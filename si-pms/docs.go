package main

import (
	"fmt"
	"net/http"
	"strconv"
)

// 서약서·동의서·교육자료(documents) 관리. 본문을 고친 뒤 "재서명 요구"를 체크하면
// 버전이 올라가 기존 서명자 전원이 다시 서명해야 하는 상태가 된다.

func (a *App) handleDocs(w http.ResponseWriter, r *http.Request) {
	docs, err := a.listDocuments(false)
	if err != nil {
		a.fail(w, err)
		return
	}
	stats := map[int64]int{}
	rows, err := a.db.Query(`SELECT k.document_id, COUNT(DISTINCT k.person_id) FROM acks k JOIN documents d ON d.id = k.document_id
		WHERE k.version = d.version GROUP BY k.document_id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for rows.Next() {
		var id int64
		var n int
		rows.Scan(&id, &n)
		stats[id] = n
	}
	rows.Close()
	a.render(w, r, "docs", "서약·교육 문서", map[string]any{"Docs": docs, "Stats": stats})
}

func (a *App) handleDocNewPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "doc", "문서 추가", map[string]any{"Doc": &Document{Kind: "training", Active: true}, "New": true})
}

func docFromForm(r *http.Request, d *Document) error {
	d.Kind = r.FormValue("kind")
	if _, ok := docKindLabel[d.Kind]; !ok {
		return fmt.Errorf("문서 종류를 선택해 주세요")
	}
	d.Title = form(r, "title")
	d.Body = form(r, "body")
	if d.Title == "" || d.Body == "" {
		return fmt.Errorf("제목과 본문은 필수입니다")
	}
	d.ValidMonths, _ = strconv.Atoi(r.FormValue("valid_months"))
	if d.ValidMonths < 0 {
		d.ValidMonths = 0
	}
	d.Sort, _ = strconv.Atoi(r.FormValue("sort"))
	d.Active = r.FormValue("active") == "1"
	return nil
}

func (a *App) handleDocCreate(w http.ResponseWriter, r *http.Request) {
	d := &Document{}
	if err := docFromForm(r, d); err != nil {
		a.renderErr(w, r, "doc", "문서 추가", err.Error(), map[string]any{"Doc": d, "New": true})
		return
	}
	res, err := a.db.Exec(`INSERT INTO documents(kind, title, body, valid_months, active, sort, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		d.Kind, d.Title, d.Body, d.ValidMonths, d.Active, d.Sort, nowTS())
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	a.audit(userFrom(r).Login, clientIP(r), "doc_create", fmt.Sprintf("doc:%d", id), d.Title)
	redirectMsg(w, r, "/docs", "문서를 추가했습니다")
}

func (a *App) handleDoc(w http.ResponseWriter, r *http.Request) {
	d, err := a.getDocument(pathID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	people, err := a.listPeople(`WHERE status IN (?, ?, ?, ?) ORDER BY company, name`, StInvited, StSubmitted, StActive, StOffboarding)
	if err != nil {
		a.fail(w, err)
		return
	}
	acks, err := a.listAcks(`WHERE k.document_id = ? AND k.version = ?`, d.ID, d.Version)
	if err != nil {
		a.fail(w, err)
		return
	}
	done := map[int64]string{}
	for _, k := range acks {
		if exp := k.ExpiresOn(); exp == "" || exp > today() {
			done[k.PersonID] = firstN(k.AckedAt, 10)
		}
	}
	a.render(w, r, "doc", d.Title, map[string]any{"Doc": d, "People": people, "Done": done, "Today": today()})
}

func (a *App) handleDocUpdate(w http.ResponseWriter, r *http.Request) {
	d, err := a.getDocument(pathID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := docFromForm(r, d); err != nil {
		redirectMsg(w, r, fmt.Sprintf("/docs/%d", d.ID), err.Error())
		return
	}
	bump := r.FormValue("bump") == "1"
	if bump {
		d.Version++
	}
	_, err = a.db.Exec(`UPDATE documents SET kind = ?, title = ?, body = ?, version = ?, valid_months = ?, active = ?, sort = ?, updated_at = ? WHERE id = ?`,
		d.Kind, d.Title, d.Body, d.Version, d.ValidMonths, d.Active, d.Sort, nowTS(), d.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	detail := d.Title
	msg := "저장했습니다"
	if bump {
		detail += fmt.Sprintf(" v%d 재서명 요구", d.Version)
		msg = fmt.Sprintf("v%d로 올렸습니다. 투입 인원 전원이 다시 서명해야 합니다", d.Version)
	}
	a.audit(userFrom(r).Login, clientIP(r), "doc_update", fmt.Sprintf("doc:%d", d.ID), detail)
	redirectMsg(w, r, fmt.Sprintf("/docs/%d", d.ID), msg)
}

// handleDocBulkAck는 집합 교육이나 서면 서약을 여러 명에게 한 번에 기록한다.
func (a *App) handleDocBulkAck(w http.ResponseWriter, r *http.Request) {
	d, err := a.getDocument(pathID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	date := form(r, "date")
	if date == "" {
		date = today()
	}
	if !validDate(date) {
		redirectMsg(w, r, fmt.Sprintf("/docs/%d", d.ID), "날짜 형식이 올바르지 않습니다")
		return
	}
	r.ParseForm()
	u := userFrom(r)
	n := 0
	for _, v := range r.Form["person_id"] {
		id, _ := strconv.ParseInt(v, 10, 64)
		p, err := a.getPerson(id)
		if err != nil || p.Status == StOffboarded {
			continue
		}
		if err := a.insertAck(p, d, p.Name, "offline", date+" 00:00:00", "", u.Name); err != nil {
			a.fail(w, err)
			return
		}
		n++
	}
	a.audit(u.Login, clientIP(r), "ack_bulk", fmt.Sprintf("doc:%d", d.ID), fmt.Sprintf("%s %d명 %s", d.Title, n, date))
	redirectMsg(w, r, fmt.Sprintf("/docs/%d", d.ID), fmt.Sprintf("%d명의 서면/집합 이행을 기록했습니다", n))
}
