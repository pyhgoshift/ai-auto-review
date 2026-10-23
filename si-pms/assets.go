package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleAssets(w http.ResponseWriter, r *http.Request) {
	status, kind, q := r.URL.Query().Get("status"), r.URL.Query().Get("kind"), form(r, "q")
	where := `WHERE 1 = 1`
	var args []any
	if status != "" {
		where += ` AND s.status = ?`
		args = append(args, status)
	} else {
		where += ` AND s.status NOT IN ('disposed', 'released')`
	}
	if kind != "" {
		where += ` AND s.kind = ?`
		args = append(args, kind)
	}
	if q != "" {
		like := "%" + q + "%"
		where += ` AND (s.asset_no LIKE ? OR s.serial LIKE ? OR s.ip LIKE ? OR s.hostname LIKE ? OR p.name LIKE ?)`
		args = append(args, like, like, like, like, like)
	}
	assets, err := a.listAssets(where+` ORDER BY s.kind, s.asset_no`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "assets", "PC·자산 관리", map[string]any{
		"Assets": assets, "Status": status, "Kind": kind, "Q": q,
		"Kinds": a.settingLines("asset_kinds"), "Statuses": []string{"stock", "assigned", "repair", "released", "disposed"},
	})
}

func (a *App) handleAssetNewPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "asset_new", "자산 등록", map[string]any{"Kinds": a.settingLines("asset_kinds"), "S": &Asset{}})
}

func assetFromForm(r *http.Request, s *Asset) {
	s.AssetNo = form(r, "asset_no")
	s.Kind = form(r, "kind")
	s.Model = form(r, "model")
	s.Serial = form(r, "serial")
	s.Hostname = form(r, "hostname")
	s.IP = form(r, "ip")
	s.MAC = form(r, "mac")
	s.Location = form(r, "location")
	s.Note = form(r, "note")
}

// checkAssetMAC은 MAC을 표준 표기로 바꾸고, 사용 중인 다른 장비와 겹치지 않는지 확인한다.
func (a *App) checkAssetMAC(s *Asset) error {
	if s.MAC == "" {
		return nil
	}
	mac, err := normalizeMAC(s.MAC)
	if err != nil {
		return err
	}
	if a.macInUse(mac, s.ID) {
		return fmt.Errorf("MAC %s 는 이미 다른 장비에 등록되어 있습니다", mac)
	}
	s.MAC = mac
	return nil
}

func (a *App) insertAsset(s *Asset, actor string) (int64, error) {
	if s.AssetNo == "" || s.Kind == "" {
		return 0, errors.New("자산번호와 종류는 필수입니다")
	}
	if err := a.checkAssetMAC(s); err != nil {
		return 0, err
	}
	ts := nowTS()
	res, err := a.db.Exec(`INSERT INTO assets(asset_no, kind, model, serial, hostname, ip, mac, location, note, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'stock', ?, ?)`,
		s.AssetNo, s.Kind, s.Model, s.Serial, s.Hostname, s.IP, s.MAC, s.Location, s.Note, ts, ts)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, fmt.Errorf("자산번호 %s 가 이미 있습니다", s.AssetNo)
		}
		return 0, err
	}
	id, _ := res.LastInsertId()
	a.db.Exec(`INSERT INTO asset_events(asset_id, kind, at, actor) VALUES(?, 'create', ?, ?)`, id, ts, actor)
	return id, nil
}

func (a *App) handleAssetCreate(w http.ResponseWriter, r *http.Request) {
	s := &Asset{}
	assetFromForm(r, s)
	u := userFrom(r)
	id, err := a.insertAsset(s, u.Name)
	if err != nil {
		a.renderErr(w, r, "asset_new", "자산 등록", err.Error(), map[string]any{"Kinds": a.settingLines("asset_kinds"), "S": s})
		return
	}
	a.audit(u.Login, clientIP(r), "asset_create", fmt.Sprintf("asset:%d", id), s.AssetNo)
	redirectMsg(w, r, fmt.Sprintf("/assets/%d", id), "자산을 등록했습니다")
}

// handleAssetImport는 엑셀에서 "CSV UTF-8"로 저장한 파일로 자산을 일괄 등록한다.
// 첫 줄은 머리글: 자산번호,종류,모델,시리얼,호스트명,IP,MAC,위치,비고
func (a *App) handleAssetImport(w http.ResponseWriter, r *http.Request) {
	f, _, err := r.FormFile("file")
	if err != nil {
		redirectMsg(w, r, "/assets/new", "CSV 파일을 선택해 주세요")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		a.fail(w, err)
		return
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	recs, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil || len(recs) < 2 {
		redirectMsg(w, r, "/assets/new", "CSV를 읽을 수 없습니다. 엑셀에서 'CSV UTF-8' 형식으로 저장했는지 확인해 주세요")
		return
	}
	u := userFrom(r)
	var ok int
	var errs []string
	for i, rec := range recs[1:] {
		get := func(n int) string {
			if n < len(rec) {
				return strings.TrimSpace(rec[n])
			}
			return ""
		}
		s := &Asset{AssetNo: get(0), Kind: get(1), Model: get(2), Serial: get(3), Hostname: get(4), IP: get(5), MAC: get(6), Location: get(7), Note: get(8)}
		if s.AssetNo == "" && s.Kind == "" {
			continue
		}
		if _, err := a.insertAsset(s, u.Name); err != nil {
			errs = append(errs, fmt.Sprintf("%d행: %v", i+2, err))
			continue
		}
		ok++
	}
	a.audit(u.Login, clientIP(r), "asset_import", "", fmt.Sprintf("%d건 등록, %d건 오류", ok, len(errs)))
	msg := fmt.Sprintf("%d건 등록했습니다", ok)
	if len(errs) > 0 {
		msg += " / 오류 " + strings.Join(errs, "; ")
	}
	redirectMsg(w, r, "/assets", msg)
}

type CheckRecord struct {
	CheckedAt, Actor, Note string
	Passed                 bool
	Results                []CheckResult
}

type CheckResult struct {
	Item string `json:"item"`
	OK   bool   `json:"ok"`
}

func (a *App) loadAsset(w http.ResponseWriter, r *http.Request) *Asset {
	list, err := a.listAssets(`WHERE s.id = ?`, pathID(r))
	if err != nil {
		a.fail(w, err)
		return nil
	}
	if len(list) == 0 {
		http.NotFound(w, r)
		return nil
	}
	return list[0]
}

func (a *App) handleAsset(w http.ResponseWriter, r *http.Request) {
	s := a.loadAsset(w, r)
	if s == nil {
		return
	}
	rows, err := a.db.Query(`SELECT checked_at, actor, note, passed, results FROM asset_checks WHERE asset_id = ? ORDER BY checked_at DESC, id DESC LIMIT 20`, s.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	var checks []*CheckRecord
	for rows.Next() {
		c := &CheckRecord{}
		var res string
		rows.Scan(&c.CheckedAt, &c.Actor, &c.Note, &c.Passed, &res)
		json.Unmarshal([]byte(res), &c.Results)
		checks = append(checks, c)
	}
	rows.Close()
	events, err := a.assetEvents(`WHERE e.asset_id = ?`, s.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	people, err := a.listPeople(`WHERE status IN (?, ?) ORDER BY company, name`, StSubmitted, StActive)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "asset", s.AssetNo, map[string]any{
		"S": s, "Checks": checks, "Events": events, "People": people,
		"CheckItems": a.settingLines("check_items"), "Kinds": a.settingLines("asset_kinds"),
	})
}

func (a *App) handleAssetUpdate(w http.ResponseWriter, r *http.Request) {
	s := a.loadAsset(w, r)
	if s == nil {
		return
	}
	assetFromForm(r, s)
	if s.AssetNo == "" || s.Kind == "" {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "자산번호와 종류는 필수입니다")
		return
	}
	if err := a.checkAssetMAC(s); err != nil {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), err.Error())
		return
	}
	_, err := a.db.Exec(`UPDATE assets SET asset_no = ?, kind = ?, model = ?, serial = ?, hostname = ?, ip = ?, mac = ?, location = ?, note = ?, updated_at = ? WHERE id = ?`,
		s.AssetNo, s.Kind, s.Model, s.Serial, s.Hostname, s.IP, s.MAC, s.Location, s.Note, nowTS(), s.ID)
	if err != nil {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "저장 실패: 자산번호 중복 여부를 확인해 주세요")
		return
	}
	u := userFrom(r)
	a.db.Exec(`INSERT INTO asset_events(asset_id, kind, at, actor) VALUES(?, 'edit', ?, ?)`, s.ID, nowTS(), u.Name)
	a.audit(u.Login, clientIP(r), "asset_update", fmt.Sprintf("asset:%d", s.ID), s.AssetNo)
	redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "저장했습니다")
}

func (a *App) assignAsset(r *http.Request, assetID int64, p *Person) error {
	if p.Status != StSubmitted && p.Status != StActive {
		return errors.New("승인대기 또는 투입중인 인력에게만 지급할 수 있습니다")
	}
	var assetNo, status string
	if err := a.db.QueryRow(`SELECT asset_no, status FROM assets WHERE id = ?`, assetID).Scan(&assetNo, &status); err != nil {
		return errors.New("지급할 자산을 선택해 주세요")
	}
	if status != "stock" {
		return fmt.Errorf("%s 는 재고 상태가 아닙니다(%s)", assetNo, assetStatusLabel[status])
	}
	u := userFrom(r)
	res, err := a.db.Exec(`UPDATE assets SET status = 'assigned', person_id = ?, assigned_at = ?, updated_at = ? WHERE id = ? AND status = 'stock'`,
		p.ID, today(), nowTS(), assetID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("다른 사용자가 먼저 처리했습니다. 새로고침 후 다시 시도해 주세요")
	}
	a.db.Exec(`INSERT INTO asset_events(asset_id, person_id, person_name, kind, at, actor) VALUES(?, ?, ?, 'assign', ?, ?)`,
		assetID, p.ID, p.Name, nowTS(), u.Name)
	if p.Status == StActive {
		if err := a.requestNAC(p.ID, assetID, u.Name); err != nil {
			return fmt.Errorf("지급은 기록했지만 IP·NAC 요청 실패: %v", err)
		}
	}
	a.audit(u.Login, clientIP(r), "asset_assign", fmt.Sprintf("asset:%d", assetID), assetNo+" → "+p.Name)
	return nil
}

func (a *App) handleAssetAssign(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	personID, _ := strconv.ParseInt(r.FormValue("person_id"), 10, 64)
	p, err := a.getPerson(personID)
	if err != nil {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", id), "지급 대상을 선택해 주세요")
		return
	}
	if err := a.assignAsset(r, id, p); err != nil {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", id), err.Error())
		return
	}
	redirectMsg(w, r, fmt.Sprintf("/assets/%d", id), p.Name+"에게 지급했습니다")
}

func (a *App) handleAssetReturn(w http.ResponseWriter, r *http.Request) {
	s := a.loadAsset(w, r)
	if s == nil {
		return
	}
	if !s.PersonID.Valid {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "지급 상태가 아닙니다")
		return
	}
	u := userFrom(r)
	note := form(r, "note")
	if _, err := a.db.Exec(`UPDATE assets SET status = 'stock', person_id = NULL, assigned_at = '', updated_at = ? WHERE id = ?`, nowTS(), s.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.releaseNAC(s.ID, u.Name)
	a.db.Exec(`INSERT INTO asset_events(asset_id, person_id, person_name, kind, at, actor, note) VALUES(?, ?, ?, 'return', ?, ?, ?)`,
		s.ID, s.PersonID.Int64, s.PersonName, nowTS(), u.Name, note)
	a.audit(u.Login, clientIP(r), "asset_return", fmt.Sprintf("asset:%d", s.ID), s.AssetNo+" ← "+s.PersonName)
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/people/") {
		back = fmt.Sprintf("/assets/%d", s.ID)
	}
	redirectMsg(w, r, back, s.AssetNo+" 반납을 기록했습니다")
}

func (a *App) handleAssetCheck(w http.ResponseWriter, r *http.Request) {
	s := a.loadAsset(w, r)
	if s == nil {
		return
	}
	r.ParseForm()
	passed := true
	var results []CheckResult
	for _, item := range a.settingLines("check_items") {
		ok := contains(r.Form["ok"], item)
		passed = passed && ok
		results = append(results, CheckResult{item, ok})
	}
	b, _ := json.Marshal(results)
	u := userFrom(r)
	if _, err := a.db.Exec(`INSERT INTO asset_checks(asset_id, checked_at, actor, passed, results, note) VALUES(?, ?, ?, ?, ?, ?)`,
		s.ID, nowTS(), u.Name, passed, string(b), form(r, "note")); err != nil {
		a.fail(w, err)
		return
	}
	label := "적합"
	if !passed {
		label = "부적합"
	}
	a.db.Exec(`INSERT INTO asset_events(asset_id, person_id, person_name, kind, at, actor, note) VALUES(?, ?, ?, 'check', ?, ?, ?)`,
		s.ID, s.PersonID, s.PersonName, nowTS(), u.Name, label)
	a.audit(u.Login, clientIP(r), "asset_check", fmt.Sprintf("asset:%d", s.ID), s.AssetNo+" "+label)
	redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "보안 점검 결과("+label+")를 기록했습니다")
}

func (a *App) handleAssetStatus(w http.ResponseWriter, r *http.Request) {
	s := a.loadAsset(w, r)
	if s == nil {
		return
	}
	to := r.FormValue("status")
	if to != "stock" && to != "repair" && to != "disposed" {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "잘못된 상태입니다")
		return
	}
	if s.Status == "assigned" {
		redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "지급 중인 자산은 먼저 반납 처리해 주세요")
		return
	}
	u := userFrom(r)
	if _, err := a.db.Exec(`UPDATE assets SET status = ?, updated_at = ? WHERE id = ?`, to, nowTS(), s.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.db.Exec(`INSERT INTO asset_events(asset_id, kind, at, actor, note) VALUES(?, 'status', ?, ?, ?)`,
		s.ID, nowTS(), u.Name, assetStatusLabel[to]+" "+form(r, "note"))
	a.audit(u.Login, clientIP(r), "asset_status", fmt.Sprintf("asset:%d", s.ID), s.AssetNo+" → "+to)
	redirectMsg(w, r, fmt.Sprintf("/assets/%d", s.ID), "상태를 "+assetStatusLabel[to]+"(으)로 바꿨습니다")
}
