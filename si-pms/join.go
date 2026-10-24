package main

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"
)

// QR 셀프 등록
//
// 현장에 붙인 QR(= /join?c=등록코드)로 들어와 정보·장비·동의·서약·교육을 한 번에 제출한다.
// 제출하면 개인 상태 페이지(/s/토큰)가 발급되고 휴대폰에 쿠키로 기억된다.
// 승인과 NAC 등록이 끝나야 그 페이지에 IP 정보가 나타난다. 즉 등록하지 않으면 네트워크를 쓸 수 없다.

const selfCookie = "pms_self"

type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (l *rateLimiter) allow(key string, max int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	now := time.Now()
	var kept []time.Time
	for _, t := range l.hits[key] {
		if now.Sub(t) < per {
			kept = append(kept, t)
		}
	}
	if len(kept) >= max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

var joinLimiter rateLimiter

func (a *App) joinURL(r *http.Request) string {
	base := strings.TrimRight(a.setting("base_url"), "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/join?c=" + url.QueryEscape(a.setting("join_code"))
}

func (a *App) validJoinCode(c string) bool {
	code := a.setting("join_code")
	return code != "" && subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1
}

func (a *App) joinData(p *Person, d map[string]any) map[string]any {
	docs, _ := a.listDocuments(true)
	var onboard []*Document
	for _, doc := range docs {
		if contains(onboardKinds, doc.Kind) {
			onboard = append(onboard, doc)
		}
	}
	out := map[string]any{"P": p, "Docs": onboard, "Customer": a.setting("customer_name"),
		"Allowed": a.settingLines("allowed_companies"), "Code": a.setting("join_code")}
	for k, v := range d {
		out[k] = v
	}
	return out
}

func (a *App) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(selfCookie); err == nil && a.personByToken(c.Value) != nil {
		http.Redirect(w, r, "/s/"+c.Value, http.StatusSeeOther)
		return
	}
	if !a.validJoinCode(r.URL.Query().Get("c")) {
		a.renderView(w, "self_invalid", a.view(r, "QR 코드 확인", map[string]any{"BadCode": true}), http.StatusNotFound)
		return
	}
	a.render(w, r, "join", "투입 등록", a.joinData(&Person{}, map[string]any{"Device": "byod"}))
}

func (a *App) handleJoinSubmit(w http.ResponseWriter, r *http.Request) {
	if !a.validJoinCode(r.FormValue("c")) {
		a.renderView(w, "self_invalid", a.view(r, "QR 코드 확인", map[string]any{"BadCode": true}), http.StatusNotFound)
		return
	}
	if !joinLimiter.allow(clientIP(r), 20, time.Hour) {
		http.Error(w, "요청이 너무 많습니다. 잠시 후 다시 시도해 주세요.", http.StatusTooManyRequests)
		return
	}
	p := &Person{}
	personFromForm(r, p)
	p.Seat, p.BadgeNo, p.Note = "", "", ""
	device := r.FormValue("device")
	dev := &Asset{Kind: byodKind, MAC: form(r, "mac"), Serial: form(r, "serial"), Model: form(r, "model"), Hostname: form(r, "hostname")}
	fail := func(msg string) {
		v := a.view(r, "투입 등록", a.joinData(p, map[string]any{"Device": device, "Dev": dev}))
		v.Err = msg
		a.renderView(w, "join", v, http.StatusBadRequest)
	}
	if err := validatePerson(p); err != nil {
		fail(err.Error())
		return
	}
	if p.Phone == "" || p.StartDate == "" || p.EndDate == "" {
		fail("연락처, 투입일, 철수예정일은 필수입니다")
		return
	}
	if p.EndDate < today() {
		fail("철수예정일이 이미 지났습니다. 날짜를 확인하거나 PMO에게 문의해 주세요")
		return
	}
	if allowed := a.settingLines("allowed_companies"); len(allowed) > 0 && !contains(allowed, p.Company) {
		fail("등록이 허용된 소속사가 아닙니다. 소속사 이름을 목록에서 선택해 주세요")
		return
	}
	var dup int
	a.db.QueryRow(`SELECT COUNT(*) FROM people WHERE name = ? AND phone = ? AND status != ?`, p.Name, p.Phone, StOffboarded).Scan(&dup)
	if dup > 0 {
		fail("이미 등록된 분입니다. 처음 등록한 휴대폰에서 QR을 다시 찍거나 PMO에게 문의해 주세요")
		return
	}
	if device == "byod" {
		if err := a.checkAssetMAC(dev); err != nil {
			fail(err.Error())
			return
		}
		if dev.Serial == "" {
			fail("반입 노트북의 시리얼 번호를 입력해 주세요")
			return
		}
	} else {
		device = "company"
	}
	docs, err := a.outstandingForNew()
	if err != nil {
		a.fail(w, err)
		return
	}
	sign := strings.Join(strings.Fields(form(r, "signature")), "")
	if sign != strings.Join(strings.Fields(p.Name), "") {
		fail("서명란에 본인 성명(" + p.Name + ")을 정확히 입력해 주세요")
		return
	}
	for _, d := range docs {
		if r.FormValue(fmt.Sprintf("agree_%d", d.ID)) != "1" {
			fail("'" + d.Title + "'에 동의(확인) 체크가 필요합니다")
			return
		}
	}

	token := randToken(24)
	ts := nowTS()
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO people(name, company, position, duty, email, phone, birth_date, start_date, end_date, status,
		token_hash, token_expires, submitted_at, note, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Company, p.Position, p.Duty, p.Email, p.Phone, p.BirthDate, p.StartDate, p.EndDate, StSubmitted,
		sha256Hex(token), addDays(p.EndDate, 30), ts, "QR 셀프 등록", ts, ts)
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	for _, d := range docs {
		if _, err := tx.Exec(`INSERT INTO acks(person_id, document_id, version, title, body, body_sha256, signed_name, method, acked_at, ip, recorded_by)
			VALUES(?, ?, ?, ?, ?, ?, ?, 'self', ?, ?, '본인')`,
			id, d.ID, d.Version, d.Title, d.Body, sha256Hex(d.Title+"\n"+d.Body), form(r, "signature"), ts, clientIP(r)); err != nil {
			a.fail(w, err)
			return
		}
	}
	if device == "byod" {
		res, err := tx.Exec(`INSERT INTO assets(asset_no, kind, model, serial, hostname, mac, status, person_id, assigned_at, note, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, 'assigned', ?, ?, '본인 반입', ?, ?)`,
			fmt.Sprintf("반입-%04d", id), byodKind, dev.Model, dev.Serial, dev.Hostname, dev.MAC, id, today(), ts, ts)
		if err != nil {
			a.fail(w, err)
			return
		}
		assetID, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO asset_events(asset_id, person_id, person_name, kind, at, actor, note) VALUES(?, ?, ?, 'assign', ?, '본인', 'QR 등록 시 반입 장비 신고')`,
			assetID, id, p.Name, ts); err != nil {
			a.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	a.audit("본인:"+p.Name, clientIP(r), "self_join", fmt.Sprintf("person:%d", id), p.Company+" / "+map[string]string{"byod": "반입 노트북", "company": "지급 PC"}[device])
	http.SetCookie(w, &http.Cookie{Name: selfCookie, Value: token, Path: "/", MaxAge: 400 * 24 * 3600,
		HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/s/"+token+"?new=1", http.StatusSeeOther)
}

// outstandingForNew는 새로 등록하는 사람이 서명해야 할 투입 문서 목록이다.
func (a *App) outstandingForNew() ([]*Document, error) {
	docs, err := a.listDocuments(true)
	if err != nil {
		return nil, err
	}
	var out []*Document
	for _, d := range docs {
		if contains(onboardKinds, d.Kind) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (a *App) handleQRPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "qr", "등록 QR", map[string]any{"URL": a.joinURL(r), "Customer": a.setting("customer_name")})
}

func (a *App) handleQRImage(w http.ResponseWriter, r *http.Request) {
	code, err := qr.Encode(a.joinURL(r), qr.M)
	if err != nil {
		a.fail(w, err)
		return
	}
	code.Scale = 10
	w.Header().Set("Content-Type", "image/png")
	w.Write(code.PNG())
}

func (a *App) handleQRRotate(w http.ResponseWriter, r *http.Request) {
	a.db.Exec(`UPDATE settings SET value = ? WHERE key = 'join_code'`, randToken(9))
	a.audit(userFrom(r).Login, clientIP(r), "join_code_rotate", "", "")
	redirectMsg(w, r, "/qr", "등록코드를 새로 만들었습니다. 이전 QR은 더 이상 쓸 수 없으니 새 QR을 출력해 붙이세요")
}
