package main

import (
	"fmt"
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
		"PoolTotal": poolTotal, "PoolUsed": poolUsed, "LastRun": a.setting("last_daily_run"),
		"Alerts": alerts, "Counts": counts, "AssetCounts": assetCounts, "ByCompany": byCompany,
		"Upcoming": upcoming, "Customer": a.setting("customer_name"), "Today": today(),
	})
}

var settingKeys = []struct{ Key, Label, Help string }{
	{"project_name", "프로젝트명", "대장 머리글에 들어갑니다"},
	{"customer_name", "고객사", ""},
	{"retention_days", "개인정보 보관일수(철수 후)", "경과하면 대시보드에 파기 알림이 뜹니다"},
	{"link_valid_days", "본인 입력 링크 유효일수", ""},
	{"base_url", "접속 주소(QR에 들어감)", "예: http://10.0.1.5:8080 — 비우면 지금 접속한 주소를 씁니다"},
	{"allowed_companies", "등록 허용 소속사", "한 줄에 하나씩. 비우면 제한 없음. QR 등록 화면에서 선택 목록으로 나옵니다"},
	{"ip_pool", "IP 대역(자동 배정)", "한 줄에 하나씩. 예: 10.0.1.10-10.0.1.200"},
	{"ip_netmask", "서브넷 마스크", ""},
	{"ip_gateway", "게이트웨이", ""},
	{"ip_dns", "DNS", "여러 개면 쉼표로 구분"},
	{"check_items", "PC 보안 점검 항목", "한 줄에 하나씩. 고객사 보안 점검표 항목과 맞추세요"},
	{"account_systems", "계정 발급 대상 시스템", "한 줄에 하나씩"},
	{"asset_kinds", "자산 종류", "한 줄에 하나씩. '데스크톱'/'노트북'은 PC 미지급 알림 기준입니다"},
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
	a.render(w, r, "settings", "설정", map[string]any{"Keys": settingKeys, "Vals": vals, "Users": users})
}

func (a *App) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	for _, s := range settingKeys {
		v := strings.TrimSpace(strings.ReplaceAll(r.FormValue(s.Key), "\r\n", "\n"))
		if s.Key == "ip_pool" {
			var lines []string
			for _, l := range strings.Split(v, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					lines = append(lines, l)
				}
			}
			if _, err := parseIPPool(lines); err != nil {
				redirectMsg(w, r, "/settings", err.Error())
				return
			}
		}
		if (s.Key == "retention_days" || s.Key == "link_valid_days") && v != "" {
			if n, err := strconv.Atoi(v); err != nil || n <= 0 {
				redirectMsg(w, r, "/settings", s.Label+"은(는) 양의 정수여야 합니다")
				return
			}
		}
		if _, err := a.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, s.Key, v); err != nil {
			a.fail(w, err)
			return
		}
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
	dir := filepath.Join(a.dataDir, "backup")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "pms-"+strings.ReplaceAll(strings.ReplaceAll(nowTS(), ":", ""), " ", "-")+".db")
	if _, err := a.db.Exec(`VACUUM INTO ?`, path); err != nil {
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
