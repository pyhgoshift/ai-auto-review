package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// NAC 등록 파일
//
// 등록 파일에 "만료일"(= 철수예정일 + 여유일)을 넣어 보내면, NAC가 그날 스스로 등록을 해제한다.
// PMO가 해제 파일 전달을 잊어도 철수자의 네트워크가 열린 채로 남지 않는다.
// 철수예정일이 바뀌면(계약 연장 등) 등록된 장비는 "만료일 변경" 요청으로 자동 분류된다.

// 요청 구분: 등록, 만료일 변경, 해제
const (
	NacKindRegister = "register"
	NacKindUpdate   = "update"
	NacKindRemove   = "remove"
)

var nacKindLabel = map[string]string{NacKindRegister: "등록", NacKindUpdate: "만료일 변경", NacKindRemove: "해제"}

// nacFieldOrder는 등록 파일에 넣을 수 있는 필드 목록이다(설정 화면 안내에도 쓴다).
var nacFieldOrder = []string{"번호", "신청구분", "MAC", "MAC(하이픈)", "MAC(구분자없음)", "IP", "성명", "소속", "역할",
	"자산번호", "장비", "모델", "시리얼", "컴퓨터이름", "사용시작", "사용종료", "만료일", "만료일시", "서브넷", "게이트웨이"}

// nacExpiry는 NAC에 넣을 만료일이다. 철수예정일이 없으면 빈 값.
func (a *App) nacExpiry(e *NacEntry) string {
	if e.EndDate == "" {
		return ""
	}
	return addDays(e.EndDate, a.settingInt0("nac_expire_buffer_days"))
}

func (a *App) nacRow(e *NacEntry, kind string, no int) map[string]string {
	exp := a.nacExpiry(e)
	expAt := ""
	if exp != "" {
		expAt = exp + " 23:59:59"
	}
	return map[string]string{
		"번호": strconv.Itoa(no), "신청구분": nacKindLabel[kind],
		"MAC": e.MAC, "MAC(하이픈)": strings.ReplaceAll(e.MAC, ":", "-"), "MAC(구분자없음)": strings.ReplaceAll(e.MAC, ":", ""),
		"IP": e.IP, "성명": e.PersonName, "소속": e.Company, "역할": a.setting("nac_role"),
		"자산번호": e.AssetNo, "장비": e.AssetKind, "모델": e.Model, "시리얼": e.Serial, "컴퓨터이름": e.Hostname,
		"사용시작": e.StartDate, "사용종료": e.EndDate, "만료일": exp, "만료일시": expAt,
		"서브넷": a.setting("ip_netmask"), "게이트웨이": a.setting("ip_gateway"),
	}
}

// nacPending은 구분별로 NAC 담당에게 보낼 대기 건을 돌려준다.
func (a *App) nacPending(kind string) ([]*NacEntry, error) {
	switch kind {
	case NacKindRegister:
		return a.listNac(`WHERE n.status = ? ORDER BY p.company, p.name`, NacRequested)
	case NacKindRemove:
		return a.listNac(`WHERE n.status = ? ORDER BY p.company, p.name`, NacRemovalRequested)
	case NacKindUpdate:
		list, err := a.listNac(`WHERE n.status = ? AND p.status = ? ORDER BY p.company, p.name`, NacRegistered, StActive)
		if err != nil {
			return nil, err
		}
		var out []*NacEntry
		for _, e := range list {
			if a.nacExpiry(e) != e.ExpireSent {
				out = append(out, e)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("알 수 없는 요청 구분: %s", kind)
}

func (a *App) nacRows(kind string) ([]map[string]string, string, error) {
	list, err := a.nacPending(kind)
	if err != nil {
		return nil, "", err
	}
	var rows []map[string]string
	for i, e := range list {
		rows = append(rows, a.nacRow(e, kind, i+1))
	}
	return rows, nacKindLabel[kind], nil
}

type nacColumn struct{ Field, Header string }

// parseNacColumns는 "필드" 또는 "필드=머리글" 줄들을 읽는다.
func parseNacColumns(lines []string) ([]nacColumn, error) {
	var cols []nacColumn
	for _, l := range lines {
		field, header, found := strings.Cut(l, "=")
		field, header = strings.TrimSpace(field), strings.TrimSpace(header)
		if !found || header == "" {
			header = field
		}
		if !contains(nacFieldOrder, field) {
			return nil, fmt.Errorf("NAC 등록 파일 열 %q 는 없는 필드입니다. 사용 가능: %s", field, strings.Join(nacFieldOrder, ", "))
		}
		cols = append(cols, nacColumn{field, header})
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("NAC 등록 파일 열을 하나 이상 지정해 주세요")
	}
	return cols, nil
}

func (a *App) nacColumns() []nacColumn {
	cols, err := parseNacColumns(a.settingLines("nac_csv_columns"))
	if err != nil {
		cols, _ = parseNacColumns(strings.Split(defaultSettings["nac_csv_columns"], "\n"))
	}
	return cols
}

func (a *App) nacCSV(rows []map[string]string) []byte {
	cols := a.nacColumns()
	var buf bytes.Buffer
	buf.WriteString("\ufeff") // 엑셀에서 한글이 깨지지 않도록 BOM
	cw := csv.NewWriter(&buf)
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = c.Header
	}
	cw.Write(head)
	for _, row := range rows {
		rec := make([]string, len(cols))
		for i, c := range cols {
			rec[i] = csvSafe(row[c.Field])
		}
		cw.Write(rec)
	}
	cw.Flush()
	return buf.Bytes()
}

func (a *App) nacScalars(s map[string]string, label string, n int) map[string]string {
	s["신청구분"] = label
	s["건수"] = strconv.Itoa(n)
	s["서브넷"] = a.setting("ip_netmask")
	s["게이트웨이"] = a.setting("ip_gateway")
	s["DNS"] = a.setting("ip_dns")
	return s
}

func (a *App) handleNac(w http.ResponseWriter, r *http.Request) {
	d := map[string]any{"Netmask": a.setting("ip_netmask"), "Gateway": a.setting("ip_gateway"), "DNS": a.setting("ip_dns"),
		"Buffer": a.settingInt0("nac_expire_buffer_days")}
	for key, kind := range map[string]string{"Register": NacKindRegister, "Update": NacKindUpdate, "Remove": NacKindRemove} {
		list, err := a.nacPending(kind)
		if err != nil {
			a.fail(w, err)
			return
		}
		var rows []nacView
		for _, e := range list {
			rows = append(rows, nacView{e, a.nacExpiry(e)})
		}
		d[key] = rows
	}
	active, err := a.listNac(`WHERE n.status = ? ORDER BY n.ip`, NacRegistered)
	if err != nil {
		a.fail(w, err)
		return
	}
	var act []nacView
	for _, e := range active {
		act = append(act, nacView{e, a.nacExpiry(e)})
	}
	sort.SliceStable(act, func(i, j int) bool { return act[i].Expiry < act[j].Expiry })
	d["Active"] = act
	total, used, poolErr := a.ipPoolStatus()
	d["PoolTotal"], d["PoolUsed"] = total, used
	if poolErr != nil {
		d["PoolErr"] = poolErr.Error()
	}
	a.render(w, r, "nac", "IP·NAC", d)
}

type nacView struct {
	*NacEntry
	Expiry string
}

func (a *App) handleNacExport(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if _, ok := nacKindLabel[kind]; !ok {
		http.NotFound(w, r)
		return
	}
	rows, label, err := a.nacRows(kind)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "export", "nac_"+kind, r.URL.Query().Get("format"))
	if r.URL.Query().Get("format") == "csv" {
		sendFile(w, "NAC_"+strings.ReplaceAll(label, " ", "")+"요청_"+today()+".csv", "text/csv; charset=utf-8", a.nacCSV(rows))
		return
	}
	a.sendHWPX(w, "nac_request", "IP·NAC_"+strings.ReplaceAll(label, " ", "")+"_신청서_"+today(),
		FillData{Scalars: a.nacScalars(a.baseScalars(r), label, len(rows)), Lists: map[string][]map[string]string{"장비": rows}})
}

// handleNacComplete는 NAC 담당이 처리를 마쳤다고 알려 오면 PMO가 한 번에 완료 처리한다.
func (a *App) handleNacComplete(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	kind := r.FormValue("kind")
	pending, err := a.nacPending(kind)
	if err != nil {
		redirectMsg(w, r, "/nac", err.Error())
		return
	}
	byID := map[int64]*NacEntry{}
	for _, e := range pending {
		byID[e.ID] = e
	}
	u := userFrom(r)
	ts := nowTS()
	n := 0
	touched := map[int64]bool{}
	for _, v := range r.Form["id"] {
		id, _ := strconv.ParseInt(v, 10, 64)
		e := byID[id]
		if e == nil {
			continue // 이미 처리되었거나 대기 건이 아님
		}
		switch kind {
		case NacKindRegister:
			a.db.Exec(`UPDATE nac_entries SET status = ?, registered_at = ?, expire_sent = ? WHERE id = ? AND status = ?`,
				NacRegistered, ts, a.nacExpiry(e), id, NacRequested)
		case NacKindUpdate:
			a.db.Exec(`UPDATE nac_entries SET expire_sent = ? WHERE id = ?`, a.nacExpiry(e), id)
		case NacKindRemove:
			a.db.Exec(`UPDATE nac_entries SET status = ?, removed_at = ? WHERE id = ? AND status = ?`, NacRemoved, ts, id, NacRemovalRequested)
			a.db.Exec(`UPDATE assets SET ip = '' WHERE id = ? AND ip = ?`, e.AssetID, e.IP)
			touched[e.PersonID] = true
		}
		a.audit(u.Login, clientIP(r), "nac_"+kind+"_done", fmt.Sprintf("person:%d", e.PersonID), e.MAC+" "+e.IP+" 만료일 "+a.nacExpiry(e))
		n++
	}
	for pid := range touched {
		a.tryCompleteOffboarding(pid, u.Name)
	}
	redirectMsg(w, r, "/nac", fmt.Sprintf("NAC %s %d건을 완료 처리했습니다", nacKindLabel[kind], n))
}
