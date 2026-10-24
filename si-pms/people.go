package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) handlePeople(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	q := form(r, "q")
	where := `WHERE 1 = 1`
	var args []any
	if status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	} else {
		where += ` AND status != ?`
		args = append(args, StOffboarded)
	}
	if q != "" {
		where += ` AND (name LIKE ? OR company LIKE ? OR duty LIKE ?)`
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	people, err := a.listPeople(where+` ORDER BY CASE status WHEN 'submitted' THEN 0 WHEN 'invited' THEN 1 WHEN 'offboarding' THEN 2 WHEN 'active' THEN 3 ELSE 4 END, company, name`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	counts := map[string]int{}
	rows, err := a.db.Query(`SELECT status, COUNT(*) FROM people GROUP BY status`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for rows.Next() {
		var s string
		var n int
		rows.Scan(&s, &n)
		counts[s] = n
	}
	rows.Close()
	a.render(w, r, "people", "인력 관리", map[string]any{
		"People": people, "Status": status, "Q": q, "Counts": counts,
		"Statuses": []string{StInvited, StSubmitted, StActive, StOffboarding, StOffboarded},
	})
}

func (a *App) companies() []string {
	rows, err := a.db.Query(`SELECT DISTINCT company FROM people WHERE company != '' ORDER BY company`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (a *App) handlePersonNewPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "person_new", "투입 요청 등록", map[string]any{"Companies": a.companies(), "P": &Person{}})
}

func personFromForm(r *http.Request, p *Person) {
	p.Name = form(r, "name")
	p.Company = form(r, "company")
	p.Position = form(r, "position")
	p.Duty = form(r, "duty")
	p.Email = form(r, "email")
	p.Phone = form(r, "phone")
	p.BirthDate = form(r, "birth_date")
	p.StartDate = form(r, "start_date")
	p.EndDate = form(r, "end_date")
	p.Seat = form(r, "seat")
	p.BadgeNo = form(r, "badge_no")
	p.Note = form(r, "note")
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func validatePerson(p *Person) error {
	if p.Name == "" || p.Company == "" {
		return errors.New("성명과 소속은 필수입니다")
	}
	for _, d := range []string{p.StartDate, p.EndDate, p.BirthDate} {
		if !validDate(d) {
			return errors.New("날짜는 YYYY-MM-DD 형식으로 입력해 주세요")
		}
	}
	if p.StartDate != "" && p.EndDate != "" && p.EndDate < p.StartDate {
		return errors.New("철수예정일이 투입일보다 빠릅니다")
	}
	return nil
}

func (a *App) handlePersonCreate(w http.ResponseWriter, r *http.Request) {
	p := &Person{}
	personFromForm(r, p)
	if err := validatePerson(p); err != nil {
		a.renderErr(w, r, "person_new", "투입 요청 등록", err.Error(), map[string]any{"Companies": a.companies(), "P": p})
		return
	}
	ts := nowTS()
	res, err := a.db.Exec(`INSERT INTO people(name, company, position, duty, email, phone, birth_date, start_date, end_date, seat, badge_no, note, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Company, p.Position, p.Duty, p.Email, p.Phone, p.BirthDate, p.StartDate, p.EndDate, p.Seat, p.BadgeNo, p.Note, StInvited, ts, ts)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	u := userFrom(r)
	a.audit(u.Login, clientIP(r), "person_create", fmt.Sprintf("person:%d", id), p.Name+" / "+p.Company)
	a.issueLinkAndShow(w, r, id)
}

// issueLinkAndShow는 본인 입력용 일회성 링크를 새로 발급하고 상세 화면에 한 번만 보여준다.
// DB에는 토큰의 해시만 저장하므로 링크를 잃어버리면 재발급해야 한다.
func (a *App) issueLinkAndShow(w http.ResponseWriter, r *http.Request, id int64) {
	token := randToken(24)
	exp := addDays(today(), a.settingInt("link_valid_days", 14))
	if _, err := a.db.Exec(`UPDATE people SET token_hash = ?, token_expires = ?, updated_at = ? WHERE id = ?`,
		sha256Hex(token), exp, nowTS(), id); err != nil {
		a.fail(w, err)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	link := fmt.Sprintf("%s://%s/s/%s", scheme, r.Host, token)
	a.audit(userFrom(r).Login, clientIP(r), "link_issue", fmt.Sprintf("person:%d", id), "만료 "+exp)
	a.showPerson(w, r, id, map[string]any{"NewLink": link, "LinkExpires": exp})
}

func (a *App) handlePerson(w http.ResponseWriter, r *http.Request) {
	a.showPerson(w, r, pathID(r), nil)
}

func (a *App) showPerson(w http.ResponseWriter, r *http.Request, id int64, extra map[string]any) {
	p, err := a.getPerson(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.fail(w, err)
		return
	}
	acks, err := a.listAcks(`WHERE k.person_id = ? ORDER BY k.acked_at DESC`, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	assets, err := a.listAssets(`WHERE s.person_id = ? ORDER BY s.asset_no`, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	stock, err := a.listAssets(`WHERE s.status = 'stock' ORDER BY s.kind, s.asset_no`)
	if err != nil {
		a.fail(w, err)
		return
	}
	accounts, err := a.listAccounts(`WHERE c.person_id = ? ORDER BY c.revoked_at != '', c.system`, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	pendOn, err := a.outstanding(id, onboardKinds)
	if err != nil {
		a.fail(w, err)
		return
	}
	pendExit, err := a.outstanding(id, exitKinds)
	if err != nil {
		a.fail(w, err)
		return
	}
	checklist, exitReady, err := a.exitChecklist(p)
	if err != nil {
		a.fail(w, err)
		return
	}
	docs, err := a.listDocuments(true)
	if err != nil {
		a.fail(w, err)
		return
	}
	nac, err := a.listNac(`WHERE n.person_id = ? ORDER BY n.id DESC`, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	history, err := a.personAssetHistory(id)
	if err != nil {
		a.fail(w, err)
		return
	}
	d := map[string]any{
		"P": p, "Acks": acks, "Assets": assets, "Stock": stock, "Accounts": accounts,
		"PendingOnboard": pendOn, "PendingExit": pendExit, "Checklist": checklist, "ExitReady": exitReady,
		"Docs": docs, "Systems": a.settingLines("account_systems"), "History": history,
		"Companies": a.companies(), "Today": today(),
		"LinkValid": p.TokenHash != "" && p.TokenExpires >= today(), "Nac": nac,
	}
	for k, v := range extra {
		d[k] = v
	}
	a.render(w, r, "person", p.Name, d)
}

type AssetEvent struct {
	At, Kind, AssetNo, AssetKind, PersonName, Actor, Note string
	AssetID                                               int64
}

var assetEventLabel = map[string]string{"assign": "지급", "return": "반납", "check": "점검", "status": "상태변경", "create": "등록", "edit": "수정"}

func (e *AssetEvent) KindLabel() string { return assetEventLabel[e.Kind] }

func (a *App) personAssetHistory(personID int64) ([]*AssetEvent, error) {
	return a.assetEvents(`WHERE e.person_id = ? AND e.kind IN ('assign', 'return')`, personID)
}

func (a *App) assetEvents(where string, args ...any) ([]*AssetEvent, error) {
	rows, err := a.db.Query(`SELECT e.at, e.kind, s.asset_no, s.kind, e.person_name, e.actor, e.note, s.id
		FROM asset_events e JOIN assets s ON s.id = e.asset_id `+where+` ORDER BY e.at DESC, e.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AssetEvent
	for rows.Next() {
		e := &AssetEvent{}
		if err := rows.Scan(&e.At, &e.Kind, &e.AssetNo, &e.AssetKind, &e.PersonName, &e.Actor, &e.Note, &e.AssetID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// loadPersonFor는 POST 핸들러 공통: 대상 인력을 읽고, 허용된 상태가 아니면 메시지와 함께 되돌린다.
func (a *App) loadPersonFor(w http.ResponseWriter, r *http.Request, allowed ...string) *Person {
	p, err := a.getPerson(pathID(r))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return nil
	} else if err != nil {
		a.fail(w, err)
		return nil
	}
	if len(allowed) > 0 && !contains(allowed, p.Status) {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "현재 상태("+p.StatusLabel()+")에서는 할 수 없는 작업입니다")
		return nil
	}
	return p
}

func (a *App) personAction(w http.ResponseWriter, r *http.Request, p *Person, action, msg, detail string) {
	a.audit(userFrom(r).Login, clientIP(r), action, fmt.Sprintf("person:%d", p.ID), p.Name+" "+detail)
	redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), msg)
}

func (a *App) handlePersonUpdate(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r)
	if p == nil {
		return
	}
	personFromForm(r, p)
	if err := validatePerson(p); err != nil {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), err.Error())
		return
	}
	_, err := a.db.Exec(`UPDATE people SET name = ?, company = ?, position = ?, duty = ?, email = ?, phone = ?, birth_date = ?,
		start_date = ?, end_date = ?, seat = ?, badge_no = ?, note = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Company, p.Position, p.Duty, p.Email, p.Phone, p.BirthDate, p.StartDate, p.EndDate, p.Seat, p.BadgeNo, p.Note, nowTS(), p.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.personAction(w, r, p, "person_update", "저장했습니다", "")
}

func (a *App) handlePersonLink(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StInvited, StSubmitted, StActive, StOffboarding)
	if p == nil {
		return
	}
	a.issueLinkAndShow(w, r, p.ID)
}

func (a *App) handlePersonApprove(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StInvited, StSubmitted)
	if p == nil {
		return
	}
	back := r.FormValue("back")
	if back != "/" {
		back = fmt.Sprintf("/people/%d", p.ID)
	}
	if err := a.activatePerson(p, userFrom(r).Name); err != nil {
		redirectMsg(w, r, back, "승인 불가("+p.Name+"): "+err.Error())
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "person_approve", fmt.Sprintf("person:%d", p.ID), p.Name)
	redirectMsg(w, r, back, p.Name+" 승인 완료. IP 배정과 NAC 등록 요청이 자동으로 만들어졌습니다")
}

// handleApproveReady는 자동 점검을 모두 통과한 승인대기 인원을 한 번에 승인한다.
func (a *App) handleApproveReady(w http.ResponseWriter, r *http.Request) {
	people, err := a.listPeople(`WHERE status = ? ORDER BY submitted_at`, StSubmitted)
	if err != nil {
		a.fail(w, err)
		return
	}
	u := userFrom(r)
	var ok, fail []string
	for _, p := range people {
		if _, ready, err := a.approvalChecks(p); err != nil || !ready {
			continue
		}
		if err := a.activatePerson(p, u.Name); err != nil {
			fail = append(fail, p.Name+"("+err.Error()+")")
			continue
		}
		a.audit(u.Login, clientIP(r), "person_approve", fmt.Sprintf("person:%d", p.ID), p.Name+" 일괄")
		ok = append(ok, p.Name)
	}
	msg := fmt.Sprintf("%d명 승인했습니다", len(ok))
	if len(fail) > 0 {
		msg += " / 실패: " + strings.Join(fail, ", ")
	}
	redirectMsg(w, r, "/", msg)
}

func (a *App) handlePersonReject(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StSubmitted)
	if p == nil {
		return
	}
	reason := form(r, "reason")
	if reason == "" {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "반려 사유를 입력해 주세요")
		return
	}
	if _, err := a.db.Exec(`UPDATE people SET status = ?, reject_reason = ?, updated_at = ? WHERE id = ?`, StInvited, reason, nowTS(), p.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.personAction(w, r, p, "person_reject", "반려했습니다. 같은 링크로 다시 입력할 수 있습니다", reason)
}

func (a *App) handlePersonOffboard(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StActive)
	if p == nil {
		return
	}
	end := form(r, "actual_end_date")
	if end == "" || !validDate(end) {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "실제 철수일을 YYYY-MM-DD 형식으로 입력해 주세요")
		return
	}
	if err := a.startOffboarding(p, end, userFrom(r).Name); err != nil {
		a.fail(w, err)
		return
	}
	a.personAction(w, r, p, "person_offboard", "철수 절차를 시작했습니다. NAC 해제 요청이 자동으로 만들어졌습니다", end)
}

func (a *App) handlePersonComplete(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StOffboarding)
	if p == nil {
		return
	}
	items, ready, err := a.exitChecklist(p)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ready {
		for _, it := range items {
			if !it.Done {
				redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "철수 완료 불가: "+it.Label+" 미완료")
				return
			}
		}
	}
	if _, err := a.db.Exec(`UPDATE people SET status = ?, token_hash = '', token_expires = '', updated_at = ? WHERE id = ?`, StOffboarded, nowTS(), p.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.personAction(w, r, p, "person_complete", "철수 완료 처리했습니다", "")
}

func (a *App) handleBadgeReturn(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StActive, StOffboarding)
	if p == nil {
		return
	}
	if _, err := a.db.Exec(`UPDATE people SET badge_returned_at = ?, updated_at = ? WHERE id = ?`, today(), nowTS(), p.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.tryCompleteOffboarding(p.ID, userFrom(r).Name)
	a.personAction(w, r, p, "badge_return", "출입증 반납을 기록했습니다", p.BadgeNo)
}

// handlePersonPurge는 보관기간이 지난 철수자의 연락처 등 개인정보를 파기한다.
// 성명·소속·투입 이력·서명 기록은 감사 증빙으로 남긴다.
func (a *App) handlePersonPurge(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StOffboarded)
	if p == nil {
		return
	}
	if _, err := a.db.Exec(`UPDATE people SET email = '', phone = '', birth_date = '', seat = '', note = '', purged_at = ?, updated_at = ? WHERE id = ?`,
		nowTS(), nowTS(), p.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.db.Exec(`UPDATE acks SET ip = '' WHERE person_id = ?`, p.ID)
	a.personAction(w, r, p, "person_purge", "개인정보(연락처·이메일·생년월일 등)를 파기했습니다", "")
}

func (a *App) handlePersonDelete(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StInvited, StSubmitted)
	if p == nil {
		return
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM assets WHERE person_id = ?`, p.ID).Scan(&n)
	if n > 0 {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "지급된 자산이 있어 삭제할 수 없습니다")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM people WHERE id = ?`, p.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "person_delete", fmt.Sprintf("person:%d", p.ID), p.Name+" / "+p.Company)
	redirectMsg(w, r, "/people", p.Name+" 투입 요청을 삭제했습니다")
}

// handleOfflineAck는 서면 서약이나 집합 교육처럼 시스템 밖에서 받은 것을 기록한다.
func (a *App) handleOfflineAck(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StInvited, StSubmitted, StActive, StOffboarding)
	if p == nil {
		return
	}
	docID, _ := strconv.ParseInt(r.FormValue("document_id"), 10, 64)
	date := form(r, "date")
	if date == "" {
		date = today()
	}
	if !validDate(date) {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "날짜 형식이 올바르지 않습니다")
		return
	}
	d, err := a.getDocument(docID)
	if err != nil {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "문서를 선택해 주세요")
		return
	}
	if err := a.insertAck(p, d, p.Name, "offline", date+" 00:00:00", "", userFrom(r).Name); err != nil {
		a.fail(w, err)
		return
	}
	a.tryCompleteOffboarding(p.ID, userFrom(r).Name)
	a.personAction(w, r, p, "ack_offline", d.Title+" 서면/집합 이행을 기록했습니다", d.Title)
}

// insertAck는 서명·이수 기록을 남긴다. 같은 날 같은 문서·버전 기록이 이미 있으면 조용히 건너뛴다(중복 방지).
func (a *App) insertAck(p *Person, d *Document, signedName, method, at, ip, by string) error {
	_, err := a.db.Exec(`INSERT OR IGNORE INTO acks(person_id, document_id, version, title, body, body_sha256, signed_name, method, acked_at, ip, recorded_by)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, d.ID, d.Version, d.Title, d.Body, sha256Hex(d.Title+"\n"+d.Body), signedName, method, at, ip, by)
	return err
}

func (a *App) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StActive)
	if p == nil {
		return
	}
	system, login := form(r, "system"), form(r, "login_id")
	issued := form(r, "issued_at")
	if issued == "" {
		issued = today()
	}
	if system == "" || login == "" || !validDate(issued) {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "시스템과 계정ID를 입력해 주세요")
		return
	}
	if _, err := a.db.Exec(`INSERT INTO accounts(person_id, system, login_id, issued_at, note) VALUES(?, ?, ?, ?, ?)`,
		p.ID, system, login, issued, form(r, "note")); err != nil {
		a.fail(w, err)
		return
	}
	a.personAction(w, r, p, "account_issue", system+" 계정 발급을 기록했습니다", system+" "+login)
}

func (a *App) handleAccountRevoke(w http.ResponseWriter, r *http.Request) {
	var personID int64
	var system, login string
	if err := a.db.QueryRow(`SELECT person_id, system, login_id FROM accounts WHERE id = ?`, pathID(r)).Scan(&personID, &system, &login); err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := a.db.Exec(`UPDATE accounts SET revoked_at = ? WHERE id = ? AND revoked_at = ''`, today(), pathID(r)); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "account_revoke", fmt.Sprintf("person:%d", personID), system+" "+login)
	a.tryCompleteOffboarding(personID, userFrom(r).Name)
	redirectMsg(w, r, fmt.Sprintf("/people/%d", personID), system+" 계정 회수를 기록했습니다")
}

func (a *App) handleAssetAssignFromPerson(w http.ResponseWriter, r *http.Request) {
	p := a.loadPersonFor(w, r, StSubmitted, StActive)
	if p == nil {
		return
	}
	assetID, _ := strconv.ParseInt(r.FormValue("asset_id"), 10, 64)
	if err := a.assignAsset(r, assetID, p); err != nil {
		redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), err.Error())
		return
	}
	redirectMsg(w, r, fmt.Sprintf("/people/%d", p.ID), "자산을 지급했습니다")
}
