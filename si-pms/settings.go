package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	alerts, err := a.alerts()
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
	assetCounts := map[string]int{}
	rows, err = a.db.Query(`SELECT status, COUNT(*) FROM assets GROUP BY status`)
	if err != nil {
		a.fail(w, err)
		return
	}
	for rows.Next() {
		var s string
		var n int
		rows.Scan(&s, &n)
		assetCounts[s] = n
	}
	rows.Close()
	byCompany := []struct {
		Company string
		N       int
	}{}
	rows, err = a.db.Query(`SELECT company, COUNT(*) FROM people WHERE status IN (?, ?) GROUP BY company ORDER BY COUNT(*) DESC`, StActive, StOffboarding)
	if err != nil {
		a.fail(w, err)
		return
	}
	for rows.Next() {
		var c string
		var n int
		rows.Scan(&c, &n)
		byCompany = append(byCompany, struct {
			Company string
			N       int
		}{c, n})
	}
	rows.Close()
	upcoming, err := a.listPeople(`WHERE (status = ? AND start_date >= ?) OR (status = ? AND end_date != '' AND end_date <= ?) ORDER BY start_date, end_date`,
		StInvited, today(), StActive, addDays(today(), 30))
	if err != nil {
		a.fail(w, err)
		return
	}
	type queueItem struct {
		P      *Person
		Checks []CheckItem
		Ready  bool
	}
	submitted, err := a.listPeople(`WHERE status = ? ORDER BY submitted_at`, StSubmitted)
	if err != nil {
		a.fail(w, err)
		return
	}
	var queue []queueItem
	readyCount := 0
	for _, p := range submitted {
		checks, ready, err := a.approvalChecks(p)
		if err != nil {
			a.fail(w, err)
			return
		}
		if ready {
			readyCount++
		}
		queue = append(queue, queueItem{p, checks, ready})
	}
	var nacReg, nacRem int
	a.db.QueryRow(`SELECT COUNT(*) FROM nac_entries WHERE status = ?`, NacRequested).Scan(&nacReg)
	a.db.QueryRow(`SELECT COUNT(*) FROM nac_entries WHERE status = ?`, NacRemovalRequested).Scan(&nacRem)
	poolTotal, poolUsed, _ := a.ipPoolStatus()
	a.render(w, r, "dashboard", "현황", map[string]any{
		"Queue": queue, "ReadyCount": readyCount, "NacReg": nacReg, "NacRem": nacRem,
		"PoolTotal": poolTotal, "PoolUsed": poolUsed, "FailedJobs": a.failedJobs(),
		"Alerts": alerts, "Counts": counts, "AssetCounts": assetCounts, "ByCompany": byCompany,
		"Upcoming": upcoming, "Customer": a.setting("customer_name"), "Today": today(),
	})
}

// 설정 항목. Kind: text(한 줄), lines(여러 줄), check(켜기/끄기)
var settingKeys = []struct{ Key, Label, Help, Kind, Group string }{
	{"project_name", "프로젝트명", "대장 머리글에 들어갑니다", "text", "기본"},
	{"customer_name", "고객사", "", "text", "기본"},
	{"retention_days", "개인정보 보관일수(철수 후)", "경과하면 대시보드에 파기 알림이 뜹니다", "text", "기본"},
	{"link_valid_days", "본인 입력 링크 유효일수", "", "text", "기본"},
	{"base_url", "접속 주소(QR에 들어감)", "예: http://10.0.1.5:8080 — 비우면 지금 접속한 주소를 씁니다", "text", "QR 등록"},
	{"allowed_companies", "등록 허용 소속사", "한 줄에 하나씩. 비우면 제한 없음. QR 등록 화면에서 선택 목록으로 나옵니다", "lines", "QR 등록"},
	{"ip_pool", "IP 대역(자동 배정)", "한 줄에 하나씩. 예: 10.0.1.10-10.0.1.200", "lines", "IP"},
	{"ip_netmask", "서브넷 마스크", "네트워크·브로드캐스트 주소는 자동으로 배정에서 뺍니다", "text", "IP"},
	{"ip_gateway", "게이트웨이", "배정에서 자동으로 뺍니다", "text", "IP"},
	{"ip_dns", "DNS", "여러 개면 쉼표로 구분. 배정에서 자동으로 뺍니다", "text", "IP"},
	{"ip_reserved", "예약 IP(배정 제외)", "한 줄에 하나씩. 서버·프린터 등. 예: 10.0.1.5 또는 10.0.1.240-10.0.1.250", "lines", "IP"},
	{"nac_csv_columns", "NAC 등록 파일 열 구성", "한 줄에 \"필드\" 또는 \"필드=파일 머리글\". 고객사 NAC 가져오기 양식과 같은 순서·머리글로 맞추세요", "lines", "NAC"},
	{"nac_expire_buffer_days", "NAC 만료일 여유(일)", "만료일 = 철수예정일 + 이 일수. 0이면 철수예정일 당일 23:59:59에 만료", "text", "NAC"},
	{"nac_role", "NAC 역할(그룹)", "등록 파일의 \"역할\" 열에 들어갈 값. 예: 프로젝트_외부인력", "text", "NAC"},
	{"auto_complete_offboarding", "철수 자동 완료", "체크리스트가 모두 채워지면 PMO 클릭 없이 철수 완료로 바꿉니다", "check", "자동화"},
	{"backup_keep", "자동 백업 보관 개수", "주 1회 백업, 이 개수보다 오래된 백업은 지웁니다", "text", "자동화"},
	{"check_items", "PC 보안 점검 항목", "한 줄에 하나씩. 고객사 보안 점검표 항목과 맞추세요", "lines", "자산"},
	{"account_systems", "계정 발급 대상 시스템", "한 줄에 하나씩", "lines", "자산"},
	{"asset_kinds", "자산 종류", "한 줄에 하나씩. '데스크톱'/'노트북'은 PC 미지급 알림 기준입니다", "lines", "자산"},
}

func splitLines(v string) []string {
	var lines []string
	for _, l := range strings.Split(v, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// validateSetting은 저장 전에 값을 검사한다. 하나라도 틀리면 아무것도 저장하지 않는다.
func validateSetting(key, label, v string) error {
	switch key {
	case "ip_pool":
		_, err := parseIPPool(splitLines(v))
		return err
	case "ip_reserved":
		if _, err := parseIPRanges(splitLines(v)); err != nil {
			return fmt.Errorf("예약 IP: %w", err)
		}
	case "ip_netmask", "ip_gateway":
		if v != "" && net.ParseIP(v).To4() == nil {
			return fmt.Errorf("%s 형식이 올바르지 않습니다: %s", label, v)
		}
	case "ip_dns":
		for _, d := range strings.Split(v, ",") {
			if d = strings.TrimSpace(d); d != "" && net.ParseIP(d).To4() == nil {
				return fmt.Errorf("DNS 형식이 올바르지 않습니다: %s", d)
			}
		}
	case "nac_csv_columns":
		_, err := parseNacColumns(splitLines(v))
		return err
	case "retention_days", "link_valid_days", "backup_keep":
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			return fmt.Errorf("%s은(는) 1 이상의 정수여야 합니다", label)
		}
	case "nac_expire_buffer_days":
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 365 {
			return fmt.Errorf("%s은(는) 0~365 사이의 정수여야 합니다", label)
		}
	}
	return nil
}

type userRow struct {
	ID                     int64
	Login, Name, Role      string
	CreatedAt, LockedUntil string
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	vals := map[string]string{}
	for _, s := range settingKeys {
		vals[s.Key] = a.setting(s.Key)
	}
	rows, err := a.db.Query(`SELECT id, login, name, role, created_at, locked_until FROM users ORDER BY id`)
	if err != nil {
		a.fail(w, err)
		return
	}
	var users []userRow
	for rows.Next() {
		var u userRow
		rows.Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.CreatedAt, &u.LockedUntil)
		users = append(users, u)
	}
	rows.Close()
	a.render(w, r, "settings", "설정", map[string]any{"Keys": settingKeys, "Vals": vals, "Users": users,
		"NacFields": nacFieldOrder, "Groups": []string{"기본", "QR 등록", "IP", "NAC", "자동화", "자산"}})
}

func (a *App) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	vals := map[string]string{}
	for _, st := range settingKeys {
		v := strings.TrimSpace(strings.ReplaceAll(r.FormValue(st.Key), "\r\n", "\n"))
		if st.Kind == "check" {
			v = "0"
			if r.FormValue(st.Key) == "1" {
				v = "1"
			}
		}
		if err := validateSetting(st.Key, st.Label, v); err != nil {
			redirectMsg(w, r, "/settings", "저장하지 않았습니다 — "+err.Error())
			return
		}
		vals[st.Key] = v
	}
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	for k, v := range vals {
		if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			a.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "settings_save", "", "")
	redirectMsg(w, r, "/settings", "설정을 저장했습니다")
}

func (a *App) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	login, name, role := form(r, "login"), form(r, "name"), r.FormValue("role")
	if login == "" || name == "" || (role != "admin" && role != "viewer") {
		redirectMsg(w, r, "/settings", "아이디, 이름, 권한을 입력해 주세요")
		return
	}
	pw := randToken(12)
	if _, err := a.db.Exec(`INSERT INTO users(login, name, role, pw_hash, must_change, created_at) VALUES(?, ?, ?, ?, 1, ?)`,
		login, name, role, hashPassword(pw), nowTS()); err != nil {
		redirectMsg(w, r, "/settings", "이미 있는 아이디입니다")
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "user_create", login, role)
	redirectMsg(w, r, "/settings", fmt.Sprintf("%s 계정을 만들었습니다. 임시 비밀번호: %s (첫 로그인 시 변경)", login, pw))
}

func (a *App) handleUserReset(w http.ResponseWriter, r *http.Request) {
	var login string
	if err := a.db.QueryRow(`SELECT login FROM users WHERE id = ?`, pathID(r)).Scan(&login); err != nil {
		http.NotFound(w, r)
		return
	}
	pw := randToken(12)
	a.db.Exec(`UPDATE users SET pw_hash = ?, must_change = 1, failed = 0, locked_until = '' WHERE id = ?`, hashPassword(pw), pathID(r))
	a.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, pathID(r))
	a.audit(userFrom(r).Login, clientIP(r), "user_reset", login, "")
	redirectMsg(w, r, "/settings", fmt.Sprintf("%s 임시 비밀번호: %s (첫 로그인 시 변경)", login, pw))
}

func (a *App) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if pathID(r) == userFrom(r).ID {
		redirectMsg(w, r, "/settings", "자기 자신은 삭제할 수 없습니다")
		return
	}
	var login string
	if err := a.db.QueryRow(`SELECT login FROM users WHERE id = ?`, pathID(r)).Scan(&login); err != nil {
		http.NotFound(w, r)
		return
	}
	a.db.Exec(`DELETE FROM users WHERE id = ?`, pathID(r))
	a.audit(userFrom(r).Login, clientIP(r), "user_delete", login, "")
	redirectMsg(w, r, "/settings", login+" 계정을 삭제했습니다")
}

type auditRow struct{ At, Actor, Action, Target, Detail, IP string }

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := form(r, "q")
	where, args := "", []any{}
	if q != "" {
		like := "%" + q + "%"
		where = `WHERE actor LIKE ? OR action LIKE ? OR target LIKE ? OR detail LIKE ?`
		args = []any{like, like, like, like}
		// "철수"처럼 한국어 작업명으로 검색해도 찾히게 한다
		for _, code := range auditCodesMatching(q) {
			where += ` OR action = ?`
			args = append(args, code)
		}
	}
	rows, err := a.db.Query(`SELECT at, actor, action, target, detail, ip FROM audit `+where+` ORDER BY id DESC LIMIT 500`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	var list []auditRow
	for rows.Next() {
		var e auditRow
		rows.Scan(&e.At, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.IP)
		list = append(list, e)
	}
	a.render(w, r, "audit", "감사 로그", map[string]any{"Rows": list, "Q": q})
}

// handleBackup은 실행 중에도 일관된 DB 사본을 만들어 내려준다.
func (a *App) handleBackup(w http.ResponseWriter, r *http.Request) {
	path, err := a.makeBackup()
	if err != nil {
		a.fail(w, err)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(userFrom(r).Login, clientIP(r), "backup", filepath.Base(path), "")
	sendFile(w, filepath.Base(path), "application/octet-stream", b)
}
