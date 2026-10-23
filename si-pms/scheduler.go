package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 자동 작업. PMO가 한 명뿐이므로 날짜가 바뀌면 사람이 누르지 않아도 다음을 처리한다.
//   1. 철수예정일이 지난 투입 인원 → 자동 철수 진행(반입 장비 반출, NAC 해제 요청)
//   2. 제출 패키지(대장 전체 + NAC 등록·해제 파일) 생성 → data/outbox/날짜.zip

func (a *App) startScheduler() {
	go func() {
		for {
			a.runDaily(false)
			time.Sleep(10 * time.Minute)
		}
	}()
}

// runDaily는 하루 한 번만 실제로 동작한다. force면 날짜와 관계없이 실행한다.
func (a *App) runDaily(force bool) {
	t := today()
	if !force && a.setting("last_daily_run") == t {
		return
	}
	n, err := a.autoOffboard()
	if err != nil {
		log.Printf("자동 철수 오류: %v", err)
	}
	path, err := a.writeOutbox()
	if err != nil {
		log.Printf("제출 패키지 오류: %v", err)
	}
	a.db.Exec(`INSERT INTO settings(key, value) VALUES('last_daily_run', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, t)
	a.audit("자동", "", "daily_run", "", fmt.Sprintf("자동 철수 %d명, 패키지 %s", n, filepath.Base(path)))
}

func (a *App) autoOffboard() (int, error) {
	people, err := a.listPeople(`WHERE status = ? AND end_date != '' AND end_date < ?`, StActive, today())
	if err != nil {
		return 0, err
	}
	for _, p := range people {
		if err := a.startOffboarding(p, p.EndDate, "자동"); err != nil {
			return 0, err
		}
		a.audit("자동", "", "person_offboard", fmt.Sprintf("person:%d", p.ID), p.Name+" 철수예정일 경과")
	}
	return len(people), nil
}

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
	for _, kind := range []string{"register", "remove"} {
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
		add("NAC/IP·NAC_"+label+"_신청서.hwpx", b)
		add("NAC/NAC_"+label+"요청.csv", nacCSV(rows))
	}
	active, err := a.listNac(`WHERE n.status = ? ORDER BY n.ip`, NacRegistered)
	if err != nil {
		return nil, err
	}
	var rows []map[string]string
	for i, e := range active {
		rows = append(rows, map[string]string{"번호": fmt.Sprint(i + 1), "신청구분": "사용중", "MAC": e.MAC, "IP": e.IP,
			"성명": e.PersonName, "소속": e.Company, "자산번호": e.AssetNo, "장비": e.AssetKind, "모델": e.Model, "시리얼": e.Serial,
			"사용시작": e.StartDate, "사용종료": e.EndDate})
	}
	add("NAC/IP_사용현황.csv", nacCSV(rows))
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

func (a *App) handleRunDaily(w http.ResponseWriter, r *http.Request) {
	a.runDaily(true)
	redirectMsg(w, r, "/exports", "자동 작업(철수 처리·제출 패키지 생성)을 지금 실행했습니다")
}
