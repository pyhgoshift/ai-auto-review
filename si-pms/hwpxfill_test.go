package main

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestMergeSplitPlaceholders(t *testing.T) {
	in := `<hp:run charPrIDRef="0"><hp:t>성명: {{성</hp:t></hp:run><hp:run charPrIDRef="3"><hp:t>명}} 끝</hp:t></hp:run>`
	got := fillSectionXML(in, FillData{Scalars: map[string]string{"성명": "홍길동"}})
	if !strings.Contains(got, "성명: 홍길동 끝") {
		t.Fatalf("쪼개진 자리표시자 미처리: %s", got)
	}
	// 다른 요소가 끼어 있으면 건드리지 않는다
	in = `<hp:t>{{성</hp:t><hp:ctrl/><hp:t>명}}</hp:t>`
	if got := mergeSplitPlaceholders(in); got != in {
		t.Fatalf("구조 변경됨: %s", got)
	}
}

func TestValueEscaping(t *testing.T) {
	got := fillSectionXML(`<hp:t>{{x}}</hp:t>`, FillData{Scalars: map[string]string{"x": "a</hp:t><evil/>&\n둘째 줄"}})
	want := `<hp:t>a&lt;/hp:t&gt;&lt;evil/&gt;&amp;<hp:lineBreak/>둘째 줄</hp:t>`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func row(n int, text string) string {
	return `<hp:tr><hp:tc><hp:subList><hp:p><hp:run><hp:t>` + text + `</hp:t></hp:run></hp:p></hp:subList>` +
		`<hp:cellAddr colAddr="0" rowAddr="` + itoa(int64(n)) + `"/><hp:cellSz width="100" height="300"/></hp:tc></hp:tr>`
}

func TestExpandTableRows(t *testing.T) {
	tbl := `<hp:tbl id="1" rowCnt="3"><hp:sz width="100" height="900"/>` + row(0, "머리") + row(1, "{{목록.이름}}") + row(2, "꼬리") + `</hp:tbl>`
	lists := map[string][]map[string]string{"목록": {{"이름": "가"}, {"이름": "나"}, {"이름": "다"}}}
	got := fillSectionXML(tbl, FillData{Lists: lists})
	for _, s := range []string{`rowCnt="5"`, `height="1500"`, `>가<`, `>나<`, `>다<`, `rowAddr="4"`} {
		if !strings.Contains(got, s) {
			t.Fatalf("%s 없음: %s", s, got)
		}
	}
	if strings.Index(got, ">다<") > strings.Index(got, ">꼬리<") {
		t.Fatal("행 순서가 틀림")
	}

	// 빈 목록이면 빈 행 하나를 남긴다
	got = fillSectionXML(tbl, FillData{Lists: map[string][]map[string]string{"목록": nil}})
	if !strings.Contains(got, `rowCnt="3"`) || strings.Contains(got, "{{") {
		t.Fatalf("빈 목록 처리 이상: %s", got)
	}
}

func TestNestedTable(t *testing.T) {
	inner := `<hp:tbl id="2" rowCnt="1"><hp:sz width="50" height="300"/>` + row(0, "{{목록.이름}}") + `</hp:tbl>`
	outer := `<hp:tbl id="1" rowCnt="1"><hp:sz width="100" height="300"/><hp:tr><hp:tc><hp:subList><hp:p><hp:run>` + inner +
		`</hp:run></hp:p></hp:subList><hp:cellAddr colAddr="0" rowAddr="0"/></hp:tc></hp:tr></hp:tbl>`
	got := fillSectionXML(outer, FillData{Lists: map[string][]map[string]string{"목록": {{"이름": "가"}, {"이름": "나"}}}})
	if !strings.Contains(got, `id="2" rowCnt="2"`) || !strings.Contains(got, `id="1" rowCnt="1"`) || !strings.Contains(got, ">나<") {
		t.Fatalf("중첩 표 처리 이상: %s", got)
	}
}

func TestBuiltinTemplatesHaveKnownPlaceholders(t *testing.T) {
	for _, l := range ledgers {
		known := map[string]bool{}
		for _, k := range commonScalars {
			known[k] = true
		}
		for _, k := range l.Scalars {
			known[k] = true
		}
		for _, c := range l.Cols {
			known[l.List+"."+c] = true
		}
		for name, cols := range l.Lists {
			for _, c := range cols {
				known[name+"."+c] = true
			}
		}
		b, err := builtinTemplates.ReadFile("hwpx/" + l.Key + ".hwpx")
		if err != nil {
			t.Fatal(err)
		}
		if err := checkHWPX(b); err != nil {
			t.Fatal(l.Key, err)
		}
		for _, m := range scalarRe.FindAllStringSubmatch(string(mustSection(t, b)), -1) {
			if !known[m[1]] {
				t.Errorf("%s: 문서화되지 않은 자리표시자 {{%s}}", l.Key, m[1])
			}
		}
	}
}

func mustSection(t *testing.T, hwpx []byte) []byte {
	t.Helper()
	out, err := fillHWPX(hwpx, FillData{}) // 재압축만 하고 내용은 그대로
	if err != nil {
		t.Fatal(err)
	}
	zr := mustZip(t, out)
	for _, f := range zr.File {
		if f.Name == "Contents/section0.xml" {
			b, _ := readZipFile(f)
			return b
		}
	}
	t.Fatal("section0 없음")
	return nil
}

func mustZip(t *testing.T, b []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}
