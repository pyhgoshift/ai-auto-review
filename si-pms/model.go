package main

import (
	"database/sql"
	"fmt"
)

// 인력 상태 흐름
//
//	invited(투입요청) → submitted(본인입력 완료) → active(투입) → offboarding(철수중) → offboarded(철수완료)
//	submitted → invited(반려)
const (
	StInvited     = "invited"
	StSubmitted   = "submitted"
	StActive      = "active"
	StOffboarding = "offboarding"
	StOffboarded  = "offboarded"
)

var statusLabel = map[string]string{
	StInvited:     "투입요청",
	StSubmitted:   "승인대기",
	StActive:      "투입중",
	StOffboarding: "철수진행",
	StOffboarded:  "철수완료",
}

var docKindLabel = map[string]string{
	"privacy":  "개인정보 동의",
	"pledge":   "보안서약",
	"training": "보안교육",
	"exit":     "철수서약",
}

var assetStatusLabel = map[string]string{
	"stock":    "재고",
	"assigned": "지급",
	"repair":   "수리",
	"disposed": "폐기",
	"released": "반출(반입장비)",
}

var methodLabel = map[string]string{"self": "전자서명", "offline": "서면/집합"}

// 투입 단계에서 받아야 하는 문서 종류와 철수 단계에서 받아야 하는 문서 종류
var onboardKinds = []string{"privacy", "pledge", "training"}
var exitKinds = []string{"exit"}

type Person struct {
	ID                                            int64
	Name, Company, Position, Duty                 string
	Email, Phone, BirthDate                       string
	StartDate, EndDate, ActualEndDate             string
	Seat, BadgeNo, BadgeReturnedAt                string
	Status, Note, RejectReason                    string
	TokenHash, TokenExpires                       string
	SubmittedAt, ApprovedBy, ApprovedAt, PurgedAt string
	CreatedAt, UpdatedAt                          string
}

const personCols = `id, name, company, position, duty, email, phone, birth_date, start_date, end_date, actual_end_date,
	seat, badge_no, badge_returned_at, status, note, reject_reason, token_hash, token_expires,
	submitted_at, approved_by, approved_at, purged_at, created_at, updated_at`

func scanPerson(s interface{ Scan(...any) error }) (*Person, error) {
	p := &Person{}
	err := s.Scan(&p.ID, &p.Name, &p.Company, &p.Position, &p.Duty, &p.Email, &p.Phone, &p.BirthDate,
		&p.StartDate, &p.EndDate, &p.ActualEndDate, &p.Seat, &p.BadgeNo, &p.BadgeReturnedAt,
		&p.Status, &p.Note, &p.RejectReason, &p.TokenHash, &p.TokenExpires,
		&p.SubmittedAt, &p.ApprovedBy, &p.ApprovedAt, &p.PurgedAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (p *Person) StatusLabel() string { return statusLabel[p.Status] }

func (a *App) getPerson(id int64) (*Person, error) {
	return scanPerson(a.db.QueryRow(`SELECT `+personCols+` FROM people WHERE id = ?`, id))
}

func (a *App) listPeople(where string, args ...any) ([]*Person, error) {
	rows, err := a.db.Query(`SELECT `+personCols+` FROM people `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type Document struct {
	ID          int64
	Kind, Title string
	Body        string
	Version     int
	ValidMonths int
	Active      bool
	Sort        int
	UpdatedAt   string
}

func (d *Document) KindLabel() string { return docKindLabel[d.Kind] }

func (a *App) listDocuments(activeOnly bool) ([]*Document, error) {
	q := `SELECT id, kind, title, body, version, valid_months, active, sort, updated_at FROM documents`
	if activeOnly {
		q += ` WHERE active = 1`
	}
	rows, err := a.db.Query(q + ` ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Document
	for rows.Next() {
		d := &Document{}
		if err := rows.Scan(&d.ID, &d.Kind, &d.Title, &d.Body, &d.Version, &d.ValidMonths, &d.Active, &d.Sort, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (a *App) getDocument(id int64) (*Document, error) {
	d := &Document{}
	err := a.db.QueryRow(`SELECT id, kind, title, body, version, valid_months, active, sort, updated_at FROM documents WHERE id = ?`, id).
		Scan(&d.ID, &d.Kind, &d.Title, &d.Body, &d.Version, &d.ValidMonths, &d.Active, &d.Sort, &d.UpdatedAt)
	return d, err
}

type Ack struct {
	ID                 int64
	PersonID           int64
	DocumentID         int64
	Version            int
	Title, Body        string
	BodySHA            string
	SignedName, Method string
	AckedAt, IP        string
	RecordedBy         string
	Kind               string // 조인으로 채움
	ValidMonths        int
}

func (k *Ack) MethodLabel() string { return methodLabel[k.Method] }

// ExpiresOn은 유효기간이 있는 문서(정기 교육 등)의 만료일. 기한이 없으면 빈 문자열.
func (k *Ack) ExpiresOn() string {
	if k.ValidMonths <= 0 {
		return ""
	}
	return addMonths(k.AckedAt, k.ValidMonths)
}

func (a *App) listAcks(where string, args ...any) ([]*Ack, error) {
	rows, err := a.db.Query(`SELECT k.id, k.person_id, k.document_id, k.version, k.title, k.body, k.body_sha256, k.signed_name,
		k.method, k.acked_at, k.ip, k.recorded_by, d.kind, d.valid_months
		FROM acks k JOIN documents d ON d.id = k.document_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Ack
	for rows.Next() {
		k := &Ack{}
		if err := rows.Scan(&k.ID, &k.PersonID, &k.DocumentID, &k.Version, &k.Title, &k.Body, &k.BodySHA, &k.SignedName,
			&k.Method, &k.AckedAt, &k.IP, &k.RecordedBy, &k.Kind, &k.ValidMonths); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// outstanding은 해당 단계에서 아직 유효한 서명·이수 기록이 없는 문서 목록을 돌려준다.
// 문서 버전이 올라갔거나 유효기간이 지났으면 다시 받아야 하는 것으로 본다.
func (a *App) outstanding(personID int64, kinds []string) ([]*Document, error) {
	docs, err := a.listDocuments(true)
	if err != nil {
		return nil, err
	}
	acks, err := a.listAcks(`WHERE k.person_id = ?`, personID)
	if err != nil {
		return nil, err
	}
	t := today()
	var out []*Document
	for _, d := range docs {
		if !contains(kinds, d.Kind) {
			continue
		}
		ok := false
		for _, k := range acks {
			if k.DocumentID == d.ID && k.Version == d.Version {
				if exp := k.ExpiresOn(); exp == "" || exp > t {
					ok = true
					break
				}
			}
		}
		if !ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type Asset struct {
	ID                           int64
	AssetNo, Kind, Model, Serial string
	Hostname, IP, MAC, Location  string
	Status                       string
	PersonID                     sql.NullInt64
	PersonName, PersonCompany    string
	AssignedAt, Note             string
	CreatedAt, UpdatedAt         string
	LastCheckAt                  string
	LastCheckPassed              sql.NullBool
}

func (s *Asset) StatusLabel() string { return assetStatusLabel[s.Status] }

func (s *Asset) CheckLabel() string {
	if !s.LastCheckPassed.Valid {
		return "미점검"
	}
	if s.LastCheckPassed.Bool {
		return "적합"
	}
	return "부적합"
}

func (a *App) listAssets(where string, args ...any) ([]*Asset, error) {
	rows, err := a.db.Query(`SELECT s.id, s.asset_no, s.kind, s.model, s.serial, s.hostname, s.ip, s.mac, s.location, s.status,
		s.person_id, COALESCE(p.name, ''), COALESCE(p.company, ''), s.assigned_at, s.note, s.created_at, s.updated_at,
		COALESCE(c.checked_at, ''), c.passed
		FROM assets s
		LEFT JOIN people p ON p.id = s.person_id
		LEFT JOIN asset_checks c ON c.id = (SELECT id FROM asset_checks WHERE asset_id = s.id ORDER BY checked_at DESC, id DESC LIMIT 1)
		`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Asset
	for rows.Next() {
		s := &Asset{}
		if err := rows.Scan(&s.ID, &s.AssetNo, &s.Kind, &s.Model, &s.Serial, &s.Hostname, &s.IP, &s.MAC, &s.Location, &s.Status,
			&s.PersonID, &s.PersonName, &s.PersonCompany, &s.AssignedAt, &s.Note, &s.CreatedAt, &s.UpdatedAt,
			&s.LastCheckAt, &s.LastCheckPassed); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type Account struct {
	ID                  int64
	PersonID            int64
	PersonName, Company string
	System, LoginID     string
	IssuedAt, RevokedAt string
	Note                string
}

func (a *App) listAccounts(where string, args ...any) ([]*Account, error) {
	rows, err := a.db.Query(`SELECT c.id, c.person_id, p.name, p.company, c.system, c.login_id, c.issued_at, c.revoked_at, c.note
		FROM accounts c JOIN people p ON p.id = c.person_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		c := &Account{}
		if err := rows.Scan(&c.ID, &c.PersonID, &c.PersonName, &c.Company, &c.System, &c.LoginID, &c.IssuedAt, &c.RevokedAt, &c.Note); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type CheckItem struct {
	Label string
	Done  bool
	Info  string
}

// exitChecklist는 철수 완료 전에 모두 충족되어야 하는 항목이다.
func (a *App) exitChecklist(p *Person) ([]CheckItem, bool, error) {
	var assets, accounts int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM assets WHERE person_id = ?`, p.ID).Scan(&assets); err != nil {
		return nil, false, err
	}
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE person_id = ? AND revoked_at = ''`, p.ID).Scan(&accounts); err != nil {
		return nil, false, err
	}
	pending, err := a.outstanding(p.ID, exitKinds)
	if err != nil {
		return nil, false, err
	}
	var nacOpen int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM nac_entries WHERE person_id = ? AND status != ?`, p.ID, NacRemoved).Scan(&nacOpen); err != nil {
		return nil, false, err
	}
	items := []CheckItem{
		{"지급 자산 전부 반납", assets == 0, fmt.Sprintf("미반납 %d건", assets)},
		{"NAC 해제·IP 회수", nacOpen == 0, fmt.Sprintf("미해제 %d건", nacOpen)},
		{"계정 전부 회수", accounts == 0, fmt.Sprintf("미회수 %d건", accounts)},
		{"출입증 반납", p.BadgeNo == "" || p.BadgeReturnedAt != "", p.BadgeNo},
		{"철수 서약", len(pending) == 0, fmt.Sprintf("미서명 %d건", len(pending))},
		{"실제 철수일 입력", p.ActualEndDate != "", ""},
	}
	all := true
	for _, it := range items {
		all = all && it.Done
	}
	return items, all, nil
}

type Alert struct {
	Level string // danger | warn | info
	Text  string
	Link  string
}

// alerts는 대시보드의 "지금 챙겨야 할 일" 목록이다. 감사 지적이 주로 나오는 누락을 먼저 보여준다.
func (a *App) alerts() ([]Alert, error) {
	var out []Alert
	t := today()
	people, err := a.listPeople(`WHERE status IN (?, ?, ?, ?) ORDER BY name`, StSubmitted, StActive, StOffboarding, StOffboarded)
	if err != nil {
		return nil, err
	}
	link := func(p *Person) string { return fmt.Sprintf("/people/%d", p.ID) }
	retention := a.settingInt("retention_days", 365)
	for _, p := range people {
		switch p.Status {
		case StSubmitted:
			out = append(out, Alert{"info", p.Name + " (" + p.Company + ") 본인 입력 완료 — 승인 필요", link(p)})
		case StActive:
			pend, err := a.outstanding(p.ID, onboardKinds)
			if err != nil {
				return nil, err
			}
			for _, d := range pend {
				out = append(out, Alert{"danger", p.Name + ": " + d.Title + " 미이행 또는 기한 만료", link(p)})
			}
			acks, err := a.listAcks(`WHERE k.person_id = ? AND d.valid_months > 0 AND d.active = 1 AND k.version = d.version`, p.ID)
			if err != nil {
				return nil, err
			}
			for _, k := range acks {
				if exp := k.ExpiresOn(); exp > t && exp <= addDays(t, 30) {
					out = append(out, Alert{"warn", p.Name + ": " + k.Title + " 유효기한 " + exp + " 도래", link(p)})
				}
			}
			var pcs int
			a.db.QueryRow(`SELECT COUNT(*) FROM assets WHERE person_id = ? AND (kind IN ('데스크톱', '노트북') OR mac != '')`, p.ID).Scan(&pcs)
			if pcs == 0 {
				out = append(out, Alert{"warn", p.Name + ": PC 미지급", link(p)})
			}
			if p.EndDate != "" && p.EndDate <= addDays(t, 14) {
				out = append(out, Alert{"warn", p.Name + ": 철수예정일 " + p.EndDate + " — 철수 절차 시작 필요", link(p)})
			}
		case StOffboarding:
			items, _, err := a.exitChecklist(p)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				if !it.Done {
					out = append(out, Alert{"danger", p.Name + " 철수: " + it.Label + " 미완료", link(p)})
				}
			}
		case StOffboarded:
			if p.PurgedAt == "" && p.ActualEndDate != "" && addDays(p.ActualEndDate, retention) <= t {
				out = append(out, Alert{"warn", p.Name + ": 개인정보 보관기간 경과 — 파기 필요", link(p)})
			}
		}
	}
	var nacReg, nacRem int
	a.db.QueryRow(`SELECT COUNT(*) FROM nac_entries WHERE status = ?`, NacRequested).Scan(&nacReg)
	a.db.QueryRow(`SELECT COUNT(*) FROM nac_entries WHERE status = ?`, NacRemovalRequested).Scan(&nacRem)
	if nacReg > 0 {
		out = append(out, Alert{"danger", fmt.Sprintf("NAC 등록 대기 %d건 — 등록 파일을 NAC 담당에게 전달 후 완료 처리", nacReg), "/nac"})
	}
	if nacRem > 0 {
		out = append(out, Alert{"danger", fmt.Sprintf("NAC 해제 대기 %d건 — 해제 파일 전달 후 완료 처리", nacRem), "/nac"})
	}
	if total, used, err := a.ipPoolStatus(); err == nil && total > 0 && total-used < 5 {
		out = append(out, Alert{"warn", fmt.Sprintf("남은 IP %d개 — 대역 추가 필요", total-used), "/settings"})
	} else if total == 0 {
		out = append(out, Alert{"warn", "IP 대역 미설정 — 승인 시 IP를 자동 배정할 수 없습니다", "/settings"})
	}
	assets, err := a.listAssets(`WHERE s.status = 'assigned' ORDER BY s.asset_no`)
	if err != nil {
		return nil, err
	}
	for _, s := range assets {
		link := fmt.Sprintf("/assets/%d", s.ID)
		switch {
		case !s.LastCheckPassed.Valid:
			out = append(out, Alert{"danger", s.AssetNo + " (" + s.PersonName + "): 보안 점검 기록 없음", link})
		case !s.LastCheckPassed.Bool:
			out = append(out, Alert{"danger", s.AssetNo + " (" + s.PersonName + "): 최근 보안 점검 부적합", link})
		case firstN(s.LastCheckAt, 10) < addDays(t, -30):
			out = append(out, Alert{"warn", s.AssetNo + " (" + s.PersonName + "): 30일 이상 미점검", link})
		}
	}
	return out, nil
}

// approvalChecks는 승인대기 인원에 대한 자동 점검 결과다. PMO는 결과를 보고 한 번만 누르면 된다.
func (a *App) approvalChecks(p *Person) ([]CheckItem, bool, error) {
	pend, err := a.outstanding(p.ID, onboardKinds)
	if err != nil {
		return nil, false, err
	}
	var items []CheckItem
	items = append(items, CheckItem{"동의·서약·교육 완료", len(pend) == 0, fmt.Sprintf("미이행 %d건", len(pend))})
	items = append(items, CheckItem{"투입일·철수예정일", p.StartDate != "" && p.EndDate != "", ""})
	if allowed := a.settingLines("allowed_companies"); len(allowed) > 0 {
		items = append(items, CheckItem{"허용된 소속사", contains(allowed, p.Company), p.Company})
	}
	assets, err := a.listAssets(`WHERE s.person_id = ? AND s.mac != ''`, p.ID)
	if err != nil {
		return nil, false, err
	}
	if len(assets) > 0 {
		dup := 0
		for _, s := range assets {
			if a.macInUse(s.MAC, s.ID) {
				dup++
			}
		}
		items = append(items, CheckItem{"장비 MAC 중복 없음", dup == 0, fmt.Sprintf("중복 %d건", dup)})
		total, used, err := a.ipPoolStatus()
		items = append(items, CheckItem{"IP 여유", err == nil && total-used >= len(assets), fmt.Sprintf("남은 IP %d개", total-used)})
	}
	ready := true
	for _, it := range items {
		ready = ready && it.Done
	}
	if len(assets) == 0 {
		items = append(items, CheckItem{"장비 없음: 승인 후 지급 PC 배정 필요", true, ""})
	}
	return items, ready, nil
}
