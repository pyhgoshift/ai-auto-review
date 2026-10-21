package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed hwpx/*.hwpx
var builtinTemplates embed.FS

type Ledger struct {
	Key, Title string
	List       string   // 표에서 반복되는 목록 이름
	Cols       []string // CSV 열 순서이자 목록 필드
	PerRecord  bool     // 개인별 문서(목록 화면에서는 출력하지 않음)
	Scalars    []string // 개인별 문서에서 쓰는 단일 값
	Lists      map[string][]string
}

var commonScalars = []string{"프로젝트명", "고객사", "출력일", "출력자"}

var ledgers = []*Ledger{
	{Key: "people", Title: "인력 투입 관리 대장", List: "인력",
		Cols: []string{"번호", "성명", "소속", "직급", "담당업무", "연락처", "이메일", "투입일", "철수예정일", "철수일", "상태", "좌석", "출입증", "승인자", "승인일"}},
	{Key: "assets", Title: "PC·자산 관리 대장", List: "자산",
		Cols: []string{"번호", "자산번호", "종류", "모델", "시리얼", "호스트명", "IP", "MAC", "위치", "상태", "사용자", "사용자소속", "지급일", "최근점검일", "점검결과"}},
	{Key: "pledges", Title: "보안서약 관리 대장", List: "서약",
		Cols: []string{"번호", "성명", "소속", "구분", "문서명", "버전", "서명일", "방식", "기록자"}},
	{Key: "trainings", Title: "보안교육 관리 대장", List: "교육",
		Cols: []string{"번호", "성명", "소속", "교육명", "버전", "이수일", "유효기한", "방식", "기록자"}},
	{Key: "accounts", Title: "계정 관리 대장", List: "계정",
		Cols: []string{"번호", "성명", "소속", "시스템", "계정ID", "발급일", "회수일", "비고"}},
	{Key: "pledge_doc", Title: "보안서약서(개인별)", PerRecord: true,
		Scalars: []string{"문서명", "본문", "성명", "소속", "직급", "담당업무", "서명일시", "서명일", "서명이름", "서명방식", "버전", "본문해시"}},
	{Key: "exit_doc", Title: "철수 보안 확인서(개인별)", PerRecord: true,
		Scalars: []string{"성명", "소속", "직급", "투입일", "철수일", "출입증", "출입증반납일"},
		Lists: map[string][]string{
			"자산": {"번호", "자산번호", "종류", "모델", "시리얼", "지급일", "반납일"},
			"계정": {"번호", "시스템", "계정ID", "발급일", "회수일"},
			"서약": {"번호", "문서명", "서명일", "방식"},
		}},
}

func ledgerByKey(key string) *Ledger {
	for _, l := range ledgers {
		if l.Key == key {
			return l
		}
	}
	return nil
}

func (a *App) baseScalars(r *http.Request) map[string]string {
	name := ""
	if u := userFrom(r); u != nil {
		name = u.Name
	}
	return map[string]string{
		"프로젝트명": a.setting("project_name"),
		"고객사":   a.setting("customer_name"),
		"출력일":   today(),
		"출력자":   name,
	}
}

func (a *App) ledgerRows(key string) ([]map[string]string, error) {
	var out []map[string]string
	add := func(m map[string]string) {
		m["번호"] = strconv.Itoa(len(out) + 1)
		out = append(out, m)
	}
	switch key {
	case "people":
		people, err := a.listPeople(`WHERE status IN (?, ?, ?) ORDER BY start_date, company, name`, StActive, StOffboarding, StOffboarded)
		if err != nil {
			return nil, err
		}
		for _, p := range people {
			add(map[string]string{"성명": p.Name, "소속": p.Company, "직급": p.Position, "담당업무": p.Duty,
				"연락처": p.Phone, "이메일": p.Email, "투입일": p.StartDate, "철수예정일": p.EndDate, "철수일": p.ActualEndDate,
				"상태": p.StatusLabel(), "좌석": p.Seat, "출입증": p.BadgeNo, "승인자": p.ApprovedBy, "승인일": firstN(p.ApprovedAt, 10)})
		}
	case "assets":
		assets, err := a.listAssets(`WHERE s.status != 'disposed' ORDER BY s.kind, s.asset_no`)
		if err != nil {
			return nil, err
		}
		for _, s := range assets {
			add(map[string]string{"자산번호": s.AssetNo, "종류": s.Kind, "모델": s.Model, "시리얼": s.Serial, "호스트명": s.Hostname,
				"IP": s.IP, "MAC": s.MAC, "위치": s.Location, "상태": s.StatusLabel(), "사용자": s.PersonName, "사용자소속": s.PersonCompany,
				"지급일": s.AssignedAt, "최근점검일": firstN(s.LastCheckAt, 10), "점검결과": s.CheckLabel()})
		}
	case "pledges", "trainings":
		cond := `d.kind != 'training'`
		if key == "trainings" {
			cond = `d.kind = 'training'`
		}
		acks, err := a.listAcks(`JOIN people p ON p.id = k.person_id WHERE ` + cond + ` ORDER BY p.company, p.name, k.acked_at`)
		if err != nil {
			return nil, err
		}
		names := map[int64]*Person{}
		for _, k := range acks {
			p, ok := names[k.PersonID]
			if !ok {
				p, err = a.getPerson(k.PersonID)
				if err != nil {
					return nil, err
				}
				names[k.PersonID] = p
			}
			add(map[string]string{"성명": p.Name, "소속": p.Company, "구분": docKindLabel[k.Kind], "문서명": k.Title, "교육명": k.Title,
				"버전": "v" + strconv.Itoa(k.Version), "서명일": firstN(k.AckedAt, 10), "이수일": firstN(k.AckedAt, 10),
				"유효기한": k.ExpiresOn(), "방식": k.MethodLabel(), "기록자": k.RecordedBy})
		}
	case "accounts":
		accounts, err := a.listAccounts(`ORDER BY p.company, p.name, c.system`)
		if err != nil {
			return nil, err
		}
		for _, c := range accounts {
			add(map[string]string{"성명": c.PersonName, "소속": c.Company, "시스템": c.System, "계정ID": c.LoginID,
				"발급일": c.IssuedAt, "회수일": c.RevokedAt, "비고": c.Note})
		}
	default:
		return nil, errors.New("unknown ledger")
	}
	return out, nil
}

// templateFor는 업로드된 고객사 양식이 있으면 그것을, 없으면 내장 기본 양식을 돌려준다.
func (a *App) templateFor(key string) ([]byte, bool, error) {
	if b, err := os.ReadFile(a.customTemplatePath(key)); err == nil {
		return b, true, nil
	}
	b, err := builtinTemplates.ReadFile("hwpx/" + key + ".hwpx")
	return b, false, err
}

func (a *App) customTemplatePath(key string) string {
	return filepath.Join(a.dataDir, "templates", key+".hwpx")
}

func sendFile(w http.ResponseWriter, name, ctype string, b []byte) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", "attachment; filename=\"download"+filepath.Ext(name)+"\"; filename*=UTF-8''"+url.PathEscape(name))
	w.Write(b)
}

func (a *App) sendHWPX(w http.ResponseWriter, key, name string, data FillData) {
	tpl, _, err := a.templateFor(key)
	if err != nil {
		a.fail(w, err)
		return
	}
	out, err := fillHWPX(tpl, data)
	if err != nil {
		a.fail(w, err)
		return
	}
	sendFile(w, name+".hwpx", "application/hwp+zip", out)
}

func (a *App) handleExports(w http.ResponseWriter, r *http.Request) {
	custom := map[string]bool{}
	for _, l := range ledgers {
		_, c, _ := a.templateFor(l.Key)
		custom[l.Key] = c
	}
	a.render(w, r, "exports", "대장 출력", map[string]any{"Ledgers": ledgers, "Custom": custom, "Common": commonScalars})
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	l := ledgerByKey(r.PathValue("key"))
	if l == nil || l.PerRecord {
		http.NotFound(w, r)
		return
	}
	rows, err := a.ledgerRows(l.Key)
	if err != nil {
		a.fail(w, err)
		return
	}
	name := l.Title + "_" + today()
	a.audit(userFrom(r).Login, clientIP(r), "export", l.Key, r.URL.Query().Get("format"))
	if r.URL.Query().Get("format") == "csv" {
		var buf bytes.Buffer
		buf.WriteString("\ufeff") // 엑셀에서 한글이 깨지지 않도록 BOM
		cw := csv.NewWriter(&buf)
		cw.Write(l.Cols)
		for _, row := range rows {
			rec := make([]string, len(l.Cols))
			for i, c := range l.Cols {
				rec[i] = csvSafe(row[c])
			}
			cw.Write(rec)
		}
		cw.Flush()
		sendFile(w, name+".csv", "text/csv; charset=utf-8", buf.Bytes())
		return
	}
	a.sendHWPX(w, l.Key, name, FillData{Scalars: a.baseScalars(r), Lists: map[string][]map[string]string{l.List: rows}})
}

// csvSafe는 엑셀 수식 주입(=, +, -, @로 시작하는 값)을 막는다.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (a *App) handleAckDoc(w http.ResponseWriter, r *http.Request) {
	acks, err := a.listAcks(`WHERE k.id = ?`, pathID(r))
	if err != nil || len(acks) == 0 {
		http.NotFound(w, r)
		return
	}
	k := acks[0]
	p, err := a.getPerson(k.PersonID)
	if err != nil {
		a.fail(w, err)
		return
	}
	s := a.baseScalars(r)
	for key, v := range map[string]string{
		"문서명": k.Title, "본문": k.Body, "성명": p.Name, "소속": p.Company, "직급": p.Position, "담당업무": p.Duty,
		"서명일시": k.AckedAt, "서명일": firstN(k.AckedAt, 10), "서명이름": k.SignedName, "서명방식": k.MethodLabel(),
		"버전": "v" + strconv.Itoa(k.Version), "본문해시": firstN(k.BodySHA, 16),
	} {
		s[key] = v
	}
	a.audit(userFrom(r).Login, clientIP(r), "export", "pledge_doc", fmt.Sprintf("ack:%d", k.ID))
	a.sendHWPX(w, "pledge_doc", k.Title+"_"+p.Name, FillData{Scalars: s})
}

func (a *App) exitDocData(r *http.Request, p *Person) (FillData, error) {
	s := a.baseScalars(r)
	for key, v := range map[string]string{
		"성명": p.Name, "소속": p.Company, "직급": p.Position, "투입일": p.StartDate, "철수일": p.ActualEndDate,
		"출입증": p.BadgeNo, "출입증반납일": p.BadgeReturnedAt,
	} {
		s[key] = v
	}
	// 지급·반납 이력을 짝지어 자산별 지급일/반납일을 만든다.
	events, err := a.assetEvents(`WHERE e.person_id = ? AND e.kind IN ('assign', 'return')`, p.ID)
	if err != nil {
		return FillData{}, err
	}
	type pair struct{ no, kind, model, serial, from, to string }
	var pairs []*pair
	open := map[int64]*pair{}
	for i := len(events) - 1; i >= 0; i-- { // 오래된 것부터
		e := events[i]
		if e.Kind == "assign" {
			list, err := a.listAssets(`WHERE s.id = ?`, e.AssetID)
			if err != nil || len(list) == 0 {
				continue
			}
			pr := &pair{no: e.AssetNo, kind: e.AssetKind, model: list[0].Model, serial: list[0].Serial, from: firstN(e.At, 10)}
			pairs = append(pairs, pr)
			open[e.AssetID] = pr
		} else if pr := open[e.AssetID]; pr != nil {
			pr.to = firstN(e.At, 10)
			delete(open, e.AssetID)
		}
	}
	var assets, accounts, pledges []map[string]string
	for i, pr := range pairs {
		to := pr.to
		if to == "" {
			to = "미반납"
		}
		assets = append(assets, map[string]string{"번호": strconv.Itoa(i + 1), "자산번호": pr.no, "종류": pr.kind,
			"모델": pr.model, "시리얼": pr.serial, "지급일": pr.from, "반납일": to})
	}
	accs, err := a.listAccounts(`WHERE c.person_id = ? ORDER BY c.system`, p.ID)
	if err != nil {
		return FillData{}, err
	}
	for i, c := range accs {
		to := c.RevokedAt
		if to == "" {
			to = "미회수"
		}
		accounts = append(accounts, map[string]string{"번호": strconv.Itoa(i + 1), "시스템": c.System, "계정ID": c.LoginID, "발급일": c.IssuedAt, "회수일": to})
	}
	acks, err := a.listAcks(`WHERE k.person_id = ? AND d.kind = 'exit' ORDER BY k.acked_at`, p.ID)
	if err != nil {
		return FillData{}, err
	}
	for i, k := range acks {
		pledges = append(pledges, map[string]string{"번호": strconv.Itoa(i + 1), "문서명": k.Title, "서명일": firstN(k.AckedAt, 10), "방식": k.MethodLabel()})
	}
	return FillData{Scalars: s, Lists: map[string][]map[string]string{"자산": assets, "계정": accounts, "서약": pledges}}, nil
}

func (a *App) handleExitDoc(w http.ResponseWriter, r *http.Request) {
	p, err := a.getPerson(pathID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := a.exitDocData(r, p)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "export", "exit_doc", fmt.Sprintf("person:%d", p.ID))
	a.sendHWPX(w, "exit_doc", "철수확인서_"+p.Name, data)
}

func (a *App) handleTemplateDownload(w http.ResponseWriter, r *http.Request) {
	l := ledgerByKey(r.PathValue("key"))
	if l == nil {
		http.NotFound(w, r)
		return
	}
	b, _, err := a.templateFor(l.Key)
	if err != nil {
		a.fail(w, err)
		return
	}
	sendFile(w, "양식_"+l.Title+".hwpx", "application/hwp+zip", b)
}

// handleTemplateUpload는 고객사 양식(HWPX)을 받아 저장한다. 시험 채우기를 해 보고 실패하면 거절한다.
func (a *App) handleTemplateUpload(w http.ResponseWriter, r *http.Request) {
	l := ledgerByKey(r.PathValue("key"))
	if l == nil {
		http.NotFound(w, r)
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		redirectMsg(w, r, "/exports", "HWPX 파일을 선택해 주세요")
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 15<<20))
	if err != nil {
		a.fail(w, err)
		return
	}
	if err := checkHWPX(b); err != nil {
		redirectMsg(w, r, "/exports", err.Error())
		return
	}
	if _, err := fillHWPX(b, FillData{Scalars: map[string]string{}, Lists: map[string][]map[string]string{}}); err != nil {
		redirectMsg(w, r, "/exports", "양식을 처리할 수 없습니다: "+err.Error())
		return
	}
	os.MkdirAll(filepath.Dir(a.customTemplatePath(l.Key)), 0o700)
	if err := os.WriteFile(a.customTemplatePath(l.Key), b, 0o600); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "template_upload", l.Key, "")
	redirectMsg(w, r, "/exports", l.Title+" 양식을 교체했습니다")
}

func (a *App) handleTemplateReset(w http.ResponseWriter, r *http.Request) {
	l := ledgerByKey(r.PathValue("key"))
	if l == nil {
		http.NotFound(w, r)
		return
	}
	os.Remove(a.customTemplatePath(l.Key))
	a.audit(userFrom(r).Login, clientIP(r), "template_reset", l.Key, "")
	redirectMsg(w, r, "/exports", l.Title+" 양식을 기본 양식으로 되돌렸습니다")
}

func checkHWPX(b []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return errors.New("HWPX 파일이 아닙니다. 한글에서 '다른 이름으로 저장 → HWPX'로 저장해 주세요(.hwp는 지원하지 않습니다)")
	}
	for _, f := range zr.File {
		if f.Name == "mimetype" {
			m, err := readZipFile(f)
			if err == nil && strings.TrimSpace(string(m)) == "application/hwp+zip" {
				return nil
			}
		}
	}
	return errors.New("HWPX 파일이 아닙니다(mimetype 불일치)")
}
