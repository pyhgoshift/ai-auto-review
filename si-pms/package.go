package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 제출 패키지: 대장 전체(HWPX+CSV), NAC 등록·만료일 변경·해제 신청서, IP 사용현황을 ZIP 하나로 묶는다.

// buildPackage는 제출용 파일 묶음(zip)을 만든다.
func (a *App) buildPackage(by string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, b []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	var summary []string
	for _, l := range ledgers {
		if l.PerRecord {
			continue
		}
		rows, err := a.ledgerRows(l.Key)
		if err != nil {
			return nil, err
		}
		b, err := a.renderHWPX(l.Key, FillData{Scalars: a.scalarsBy(by), Lists: map[string][]map[string]string{l.List: rows}})
		if err != nil {
			return nil, err
		}
		if err := add("대장/"+l.Title+".hwpx", b); err != nil {
			return nil, err
		}
		if err := add("대장/"+l.Title+".csv", ledgerCSV(l, rows)); err != nil {
			return nil, err
		}
		summary = append(summary, fmt.Sprintf("%s: %d건", l.Title, len(rows)))
	}
	for _, kind := range []string{NacKindRegister, NacKindUpdate, NacKindRemove} {
		rows, label, err := a.nacRows(kind)
		if err != nil {
			return nil, err
		}
		summary = append(summary, fmt.Sprintf("NAC %s 요청: %d건", label, len(rows)))
		if len(rows) == 0 {
			continue
		}
		b, err := a.renderHWPX("nac_request", FillData{Scalars: a.nacScalars(a.scalarsBy(by), label, len(rows)),
			Lists: map[string][]map[string]string{"장비": rows}})
		if err != nil {
			return nil, err
		}
		name := strings.ReplaceAll(label, " ", "")
		add("NAC/IP·NAC_"+name+"_신청서.hwpx", b)
		add("NAC/NAC_"+name+"요청.csv", a.nacCSV(rows))
	}
	active, err := a.listNac(`WHERE n.status = ? ORDER BY n.ip`, NacRegistered)
	if err != nil {
		return nil, err
	}
	var rows []map[string]string
	for i, e := range active {
		row := a.nacRow(e, "", i+1)
		row["신청구분"] = "사용중"
		rows = append(rows, row)
	}
	add("NAC/IP_사용현황.csv", a.nacCSV(rows))
	summary = append(summary, fmt.Sprintf("IP 사용중: %d건", len(rows)))
	readme := fmt.Sprintf("%s 제출 패키지\r\n생성: %s (%s)\r\n\r\n%s\r\n", a.setting("project_name"), nowTS(), by,
		strings.Join(summary, "\r\n"))
	add("목록.txt", []byte(readme))
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (a *App) outboxDir() string { return filepath.Join(a.dataDir, "outbox") }

func (a *App) writeOutbox() (string, error) {
	b, err := a.buildPackage("자동")
	if err != nil {
		return "", err
	}
	os.MkdirAll(a.outboxDir(), 0o700)
	path := filepath.Join(a.outboxDir(), "제출패키지_"+today()+".zip")
	return path, os.WriteFile(path, b, 0o600)
}

func (a *App) outboxFiles() []string {
	entries, _ := os.ReadDir(a.outboxDir())
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".zip") {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

func (a *App) handlePackage(w http.ResponseWriter, r *http.Request) {
	b, err := a.buildPackage(userFrom(r).Name)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "export", "package", "")
	sendFile(w, "제출패키지_"+today()+".zip", "application/zip", b)
}

func (a *App) handleOutboxFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	for _, f := range a.outboxFiles() {
		if f == name {
			b, err := os.ReadFile(filepath.Join(a.outboxDir(), f))
			if err != nil {
				a.fail(w, err)
				return
			}
			a.audit(userFrom(r).Login, clientIP(r), "export", "outbox", f)
			sendFile(w, f, "application/zip", b)
			return
		}
	}
	http.NotFound(w, r)
}
