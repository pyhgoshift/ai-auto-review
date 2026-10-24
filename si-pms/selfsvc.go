package main

import (
	"fmt"
	"net/http"
	"strings"
)

// 투입 인력 본인용 화면. 로그인 대신 PMO가 발급한 개인 링크(토큰)로 접근한다.
// 투입 전: 본인 정보 확인 + 개인정보 동의 + 보안서약 + 보안교육 확인
// 투입 중: 만료되었거나 버전이 바뀐 서약·교육 재이행
// 철수 중: 철수 서약

func (a *App) personByToken(token string) *Person {
	if len(token) < 20 {
		return nil
	}
	list, err := a.listPeople(`WHERE token_hash = ? AND token_expires >= ? AND status != ?`, sha256Hex(token), today(), StOffboarded)
	if err != nil || len(list) != 1 {
		return nil
	}
	return list[0]
}

func selfKinds(p *Person) []string {
	if p.Status == StOffboarding {
		return exitKinds
	}
	return onboardKinds
}

func (a *App) selfView(w http.ResponseWriter, r *http.Request, p *Person, errMsg, done string) {
	pend, err := a.outstanding(p.ID, selfKinds(p))
	if err != nil {
		a.fail(w, err)
		return
	}
	nac, err := a.listNac(`WHERE n.person_id = ? AND n.status != ? ORDER BY n.id`, p.ID, NacRemoved)
	if err != nil {
		a.fail(w, err)
		return
	}
	ready := false
	for _, e := range nac {
		ready = ready || e.Status == NacRegistered
	}
	v := a.view(r, "투입 인력 본인 확인", map[string]any{
		"P": p, "Pending": pend, "Done": done,
		"EditInfo": p.Status == StInvited || (p.Status == StSubmitted && len(pend) > 0),
		"Customer": a.setting("customer_name"), "Nac": nac, "NetReady": ready,
		"Netmask": a.setting("ip_netmask"), "Gateway": a.setting("ip_gateway"), "DNS": a.setting("ip_dns"),
		"New": r.URL.Query().Get("new") == "1",
	})
	v.Err = errMsg
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusBadRequest
	}
	a.renderView(w, "self", v, status)
}

func (a *App) handleSelfPage(w http.ResponseWriter, r *http.Request) {
	p := a.personByToken(r.PathValue("token"))
	if p == nil {
		a.renderView(w, "self_invalid", a.view(r, "링크 만료", nil), http.StatusNotFound)
		return
	}
	a.selfView(w, r, p, "", "")
}

func (a *App) handleSelfSubmit(w http.ResponseWriter, r *http.Request) {
	p := a.personByToken(r.PathValue("token"))
	if p == nil {
		a.renderView(w, "self_invalid", a.view(r, "링크 만료", nil), http.StatusNotFound)
		return
	}
	r.ParseForm()
	editInfo := p.Status == StInvited || p.Status == StSubmitted
	if editInfo {
		p.Position = form(r, "position")
		p.Duty = form(r, "duty")
		p.Phone = form(r, "phone")
		p.Email = form(r, "email")
		p.BirthDate = form(r, "birth_date")
		if p.Phone == "" {
			a.selfView(w, r, p, "연락처는 필수입니다", "")
			return
		}
		if !validDate(p.BirthDate) {
			a.selfView(w, r, p, "생년월일은 YYYY-MM-DD 형식으로 입력해 주세요", "")
			return
		}
	}
	pend, err := a.outstanding(p.ID, selfKinds(p))
	if err != nil {
		a.fail(w, err)
		return
	}
	// 서명은 본인 성명을 직접 입력하는 방식(전자서명 대용). 공백 차이는 무시한다.
	sign := strings.Join(strings.Fields(form(r, "signature")), "")
	if len(pend) > 0 && sign != strings.Join(strings.Fields(p.Name), "") {
		a.selfView(w, r, p, "서명란에 본인 성명("+p.Name+")을 정확히 입력해 주세요", "")
		return
	}
	for _, d := range pend {
		if r.FormValue(fmt.Sprintf("agree_%d", d.ID)) != "1" {
			a.selfView(w, r, p, "'"+d.Title+"'에 동의(확인) 체크가 필요합니다", "")
			return
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	ts := nowTS()
	for _, d := range pend {
		// 제출 버튼을 두 번 눌러도 같은 날 같은 문서 서명은 한 번만 남는다
		if _, err := tx.Exec(`INSERT OR IGNORE INTO acks(person_id, document_id, version, title, body, body_sha256, signed_name, method, acked_at, ip, recorded_by)
			VALUES(?, ?, ?, ?, ?, ?, ?, 'self', ?, ?, '본인')`,
			p.ID, d.ID, d.Version, d.Title, d.Body, sha256Hex(d.Title+"\n"+d.Body), form(r, "signature"), ts, clientIP(r)); err != nil {
			a.fail(w, err)
			return
		}
	}
	if editInfo {
		if _, err := tx.Exec(`UPDATE people SET position = ?, duty = ?, phone = ?, email = ?, birth_date = ?, status = ?, submitted_at = ?, updated_at = ? WHERE id = ?`,
			p.Position, p.Duty, p.Phone, p.Email, p.BirthDate, StSubmitted, ts, ts, p.ID); err != nil {
			a.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	a.audit("본인:"+p.Name, clientIP(r), "self_submit", fmt.Sprintf("person:%d", p.ID), fmt.Sprintf("서명 %d건", len(pend)))
	a.tryCompleteOffboarding(p.ID, "본인:"+p.Name)
	if editInfo {
		p.Status = StSubmitted
	}
	done := "제출되었습니다. 담당자 승인 후 PC와 계정이 지급됩니다."
	if p.Status == StActive {
		done = "재서명·교육 확인이 완료되었습니다."
	} else if p.Status == StOffboarding {
		done = "철수 서약이 완료되었습니다. 그동안 수고 많으셨습니다."
	}
	a.selfView(w, r, p, "", done)
}
