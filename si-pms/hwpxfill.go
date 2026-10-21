package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// HWPX 양식 채우기
//
// HWPX는 ZIP 안에 XML(Contents/section*.xml)이 들어 있는 한글 문서 형식이다.
// 한글에서 양식을 만들고 아래 자리표시자를 입력해 두면 값이 채워진다.
//
//	{{키}}         단일 값. 값에 줄바꿈이 있으면 줄바꿈으로 들어간다.
//	{{목록.필드}}   표의 한 행 안에 두면 목록 항목 수만큼 그 행이 복제된다.
//
// 한글은 입력 도중 서식이 바뀌면 "{{성" + "명}}" 처럼 글자를 여러 run으로 쪼개 저장하는데,
// 그런 경우도 run 경계만 있는 조각이면 이어 붙여 처리한다.

type FillData struct {
	Scalars map[string]string
	Lists   map[string][]map[string]string
}

var (
	// run/t 경계 하나: </hp:t>[</hp:run><hp:run ...>]<hp:t>
	boundary  = `</hp:t>(?:</hp:run><hp:run[^>/]*>)?<hp:t>`
	splitRe   = regexp.MustCompile(`\{(?:` + boundary + `)?\{(?:[^{}<]|` + boundary + `)*?\}(?:` + boundary + `)?\}`)
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	scalarRe  = regexp.MustCompile(`\{\{([^{}<>]+?)\}\}`)
	listRe    = regexp.MustCompile(`\{\{([^{}<>.]+?)\.([^{}<>]+?)\}\}`)
	linesegRe = regexp.MustCompile(`<hp:linesegarray>.*?</hp:linesegarray>|<hp:linesegarray/>`)
	rowCntRe  = regexp.MustCompile(`rowCnt="\d+"`)
	rowAddrRe = regexp.MustCompile(`rowAddr="\d+"`)
	cellHRe   = regexp.MustCompile(`<hp:cellSz [^>]*height="(\d+)"`)
	tblSzRe   = regexp.MustCompile(`(<hp:sz [^>]*height=")(\d+)(")`)
	sectionRe = regexp.MustCompile(`^Contents/(section\d+|header|masterpage\d*)\.xml$`)
)

func fillHWPX(tpl []byte, data FillData) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(tpl), int64(len(tpl)))
	if err != nil {
		return nil, fmt.Errorf("HWPX(zip) 파일이 아닙니다: %w", err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	wroteMime := false
	write := func(name string, content []byte, store bool) error {
		h := &zip.FileHeader{Name: name, CRC32: crc32.ChecksumIEEE(content), UncompressedSize64: uint64(len(content))}
		var body []byte
		if store {
			h.Method = zip.Store
			body = content
		} else {
			h.Method = zip.Deflate
			var buf bytes.Buffer
			fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
			fw.Write(content)
			fw.Close()
			body = buf.Bytes()
		}
		h.CompressedSize64 = uint64(len(body))
		w, err := zw.CreateRaw(h)
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	// 한글은 mimetype 항목이 맨 앞에, 압축 없이 있어야 연다.
	for _, f := range zr.File {
		if f.Name == "mimetype" {
			b, err := readZipFile(f)
			if err != nil {
				return nil, err
			}
			if err := write("mimetype", b, true); err != nil {
				return nil, err
			}
			wroteMime = true
		}
	}
	if !wroteMime {
		return nil, errors.New("HWPX 양식이 아닙니다(mimetype 없음)")
	}
	for _, f := range zr.File {
		if f.Name == "mimetype" {
			continue
		}
		b, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		switch {
		case sectionRe.MatchString(f.Name) && f.Name != "Contents/header.xml":
			b = []byte(fillSectionXML(string(b), data))
		case f.Name == "Preview/PrvText.txt":
			b = []byte(scalarRe.ReplaceAllStringFunc(string(b), func(m string) string {
				if v, ok := data.Scalars[m[2:len(m)-2]]; ok {
					return v
				}
				return m
			}))
		}
		if err := write(f.Name, b, false); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 50<<20))
}

func fillSectionXML(x string, data FillData) string {
	x = mergeSplitPlaceholders(x)
	x = expandTables(x, data.Lists)
	x = scalarRe.ReplaceAllStringFunc(x, func(m string) string {
		if v, ok := data.Scalars[strings.TrimSpace(m[2:len(m)-2])]; ok {
			return xmlText(v)
		}
		return m // 모르는 자리표시자는 그대로 두어 오타를 알아챌 수 있게 한다
	})
	// 줄 배치 캐시를 지워 한글이 문서를 열 때 다시 계산하게 한다.
	return linesegRe.ReplaceAllString(x, "")
}

func mergeSplitPlaceholders(x string) string {
	return splitRe.ReplaceAllStringFunc(x, func(m string) string {
		if !strings.Contains(m, "<") {
			return m
		}
		return tagRe.ReplaceAllString(m, "")
	})
}

// xmlText는 값을 hp:t 안에 넣을 수 있는 형태로 바꾼다.
func xmlText(v string) string {
	var b strings.Builder
	v = strings.ReplaceAll(v, "\r\n", "\n")
	for _, r := range v {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\n':
			b.WriteString("<hp:lineBreak/>")
		case r == '\t':
			b.WriteString(" ")
		case r < 0x20 || r == 0xFFFE || r == 0xFFFF:
			// XML에 쓸 수 없는 제어문자는 버린다
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// topLevel은 s 안에서 같은 이름 요소의 중첩을 고려해 최상위 요소들의 [시작, 끝) 범위를 찾는다.
func topLevel(s, tag string) [][2]int {
	open, closeTag := "<"+tag, "</"+tag+">"
	var out [][2]int
	depth, start := 0, 0
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], closeTag) {
			depth--
			i += len(closeTag)
			if depth == 0 {
				out = append(out, [2]int{start, i})
			}
			continue
		}
		if strings.HasPrefix(s[i:], open) && i+len(open) < len(s) {
			c := s[i+len(open)]
			if c == ' ' || c == '>' || c == '/' {
				end := strings.IndexByte(s[i:], '>')
				if end < 0 {
					break
				}
				selfClosing := s[i+end-1] == '/'
				if !selfClosing {
					if depth == 0 {
						start = i
					}
					depth++
				}
				i += end + 1
				continue
			}
		}
		i++
	}
	return out
}

func expandTables(x string, lists map[string][]map[string]string) string {
	spans := topLevel(x, "hp:tbl")
	if len(spans) == 0 {
		return x
	}
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(x[prev:sp[0]])
		b.WriteString(expandTable(x[sp[0]:sp[1]], lists))
		prev = sp[1]
	}
	b.WriteString(x[prev:])
	return b.String()
}

func expandTable(tbl string, lists map[string][]map[string]string) string {
	openEnd := strings.IndexByte(tbl, '>') + 1
	head, body := tbl[:openEnd], tbl[openEnd:]
	rows := topLevel(body, "hp:tr")
	if len(rows) == 0 {
		return tbl
	}
	var newRows []string
	changed, expanded := false, false
	extraHeight := 0
	for _, sp := range rows {
		row := body[sp[0]:sp[1]]
		// 셀 안의 중첩 표는 먼저 처리한다
		if strings.Contains(row[4:], "<hp:tbl") {
			if nested := expandTables(row, lists); nested != row {
				row, changed = nested, true
			}
		}
		name := ""
		for _, m := range listRe.FindAllStringSubmatch(row, -1) {
			if _, ok := lists[m[1]]; ok {
				name = m[1]
				break
			}
		}
		if name == "" {
			newRows = append(newRows, row)
			continue
		}
		expanded = true
		items := lists[name]
		if len(items) == 0 {
			items = []map[string]string{{}} // 빈 행 하나는 남겨 양식 모양을 유지
		}
		for _, item := range items {
			newRows = append(newRows, listRe.ReplaceAllStringFunc(row, func(m string) string {
				sm := listRe.FindStringSubmatch(m)
				if sm[1] != name {
					return m
				}
				return xmlText(item[strings.TrimSpace(sm[2])])
			}))
		}
		if h := cellHRe.FindStringSubmatch(row); h != nil {
			n, _ := strconv.Atoi(h[1])
			extraHeight += n * (len(items) - 1)
		}
	}
	if !changed && !expanded {
		return tbl
	}
	// 행이 늘었으면 행 주소(rowAddr)를 다시 매기고 표의 행 수와 높이를 고친다.
	var nb strings.Builder
	nb.WriteString(body[:rows[0][0]])
	for i, row := range newRows {
		if expanded && !strings.Contains(row[4:], "<hp:tbl") {
			row = rowAddrRe.ReplaceAllString(row, fmt.Sprintf(`rowAddr="%d"`, i))
		}
		nb.WriteString(row)
	}
	nb.WriteString(body[rows[len(rows)-1][1]:])
	newBody := nb.String()
	if !expanded {
		return head + newBody
	}
	head = rowCntRe.ReplaceAllString(head, fmt.Sprintf(`rowCnt="%d"`, len(newRows)))
	if loc := tblSzRe.FindStringSubmatchIndex(newBody); loc != nil && extraHeight != 0 {
		h, _ := strconv.Atoi(newBody[loc[4]:loc[5]])
		newBody = newBody[:loc[4]] + strconv.Itoa(h+extraHeight) + newBody[loc[5]:]
	}
	return head + newBody
}
