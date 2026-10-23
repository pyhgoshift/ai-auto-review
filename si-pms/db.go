package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings(
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS users(
	id INTEGER PRIMARY KEY,
	login TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'admin',
	pw_hash TEXT NOT NULL,
	must_change INTEGER NOT NULL DEFAULT 0,
	failed INTEGER NOT NULL DEFAULT 0,
	locked_until TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions(
	token_hash TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	csrf TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS people(
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	company TEXT NOT NULL DEFAULT '',
	position TEXT NOT NULL DEFAULT '',
	duty TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	phone TEXT NOT NULL DEFAULT '',
	birth_date TEXT NOT NULL DEFAULT '',
	start_date TEXT NOT NULL DEFAULT '',
	end_date TEXT NOT NULL DEFAULT '',
	actual_end_date TEXT NOT NULL DEFAULT '',
	seat TEXT NOT NULL DEFAULT '',
	badge_no TEXT NOT NULL DEFAULT '',
	badge_returned_at TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'invited',
	note TEXT NOT NULL DEFAULT '',
	reject_reason TEXT NOT NULL DEFAULT '',
	token_hash TEXT NOT NULL DEFAULT '',
	token_expires TEXT NOT NULL DEFAULT '',
	submitted_at TEXT NOT NULL DEFAULT '',
	approved_by TEXT NOT NULL DEFAULT '',
	approved_at TEXT NOT NULL DEFAULT '',
	purged_at TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS people_token ON people(token_hash);
CREATE TABLE IF NOT EXISTS documents(
	id INTEGER PRIMARY KEY,
	kind TEXT NOT NULL,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	version INTEGER NOT NULL DEFAULT 1,
	valid_months INTEGER NOT NULL DEFAULT 0,
	active INTEGER NOT NULL DEFAULT 1,
	sort INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS acks(
	id INTEGER PRIMARY KEY,
	person_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
	document_id INTEGER NOT NULL REFERENCES documents(id),
	version INTEGER NOT NULL,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	body_sha256 TEXT NOT NULL,
	signed_name TEXT NOT NULL,
	method TEXT NOT NULL,
	acked_at TEXT NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	recorded_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS acks_person ON acks(person_id);
CREATE TABLE IF NOT EXISTS assets(
	id INTEGER PRIMARY KEY,
	asset_no TEXT UNIQUE NOT NULL,
	kind TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	serial TEXT NOT NULL DEFAULT '',
	hostname TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT '',
	mac TEXT NOT NULL DEFAULT '',
	location TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'stock',
	person_id INTEGER REFERENCES people(id),
	assigned_at TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS asset_events(
	id INTEGER PRIMARY KEY,
	asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	person_id INTEGER,
	person_name TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL,
	at TEXT NOT NULL,
	actor TEXT NOT NULL,
	note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS asset_events_person ON asset_events(person_id);
CREATE TABLE IF NOT EXISTS asset_checks(
	id INTEGER PRIMARY KEY,
	asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	checked_at TEXT NOT NULL,
	actor TEXT NOT NULL,
	passed INTEGER NOT NULL,
	results TEXT NOT NULL,
	note TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS accounts(
	id INTEGER PRIMARY KEY,
	person_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
	system TEXT NOT NULL,
	login_id TEXT NOT NULL,
	issued_at TEXT NOT NULL,
	revoked_at TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS nac_entries(
	id INTEGER PRIMARY KEY,
	person_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
	asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	mac TEXT NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	requested_at TEXT NOT NULL,
	registered_at TEXT NOT NULL DEFAULT '',
	removal_requested_at TEXT NOT NULL DEFAULT '',
	removed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS nac_status ON nac_entries(status);
CREATE TABLE IF NOT EXISTS audit(
	id INTEGER PRIMARY KEY,
	at TEXT NOT NULL,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT ''
);
`

var defaultSettings = map[string]string{
	"project_name":    "OO 시스템 구축 프로젝트",
	"customer_name":   "OO기관",
	"retention_days":  "365",
	"link_valid_days": "14",
	"check_items": strings.Join([]string{
		"백신 설치 및 최신 엔진",
		"매체제어(USB 등) 적용",
		"DLP/보안 에이전트 설치",
		"화면보호기 10분 + 암호",
		"OS 보안 패치 최신",
		"공유폴더 미사용",
		"BIOS/CMOS 암호 설정",
	}, "\n"),
	"account_systems": strings.Join([]string{
		"AD(도메인 계정)", "VPN", "형상관리(Git)", "DB 접근제어", "개발 서버", "메신저/메일",
	}, "\n"),
	"asset_kinds":       strings.Join([]string{"데스크톱", "노트북", byodKind, "모니터", "보안USB", "기타"}, "\n"),
	"allowed_companies": "",
	"ip_pool":           "",
	"ip_netmask":        "255.255.255.0",
	"ip_gateway":        "",
	"ip_dns":            "",
	"base_url":          "",
	"last_daily_run":    "",
}

type seedDoc struct {
	kind, title, body string
	validMonths       int
}

var defaultDocs = []seedDoc{
	{"privacy", "개인정보 수집·이용 동의서", `본 프로젝트는 투입 인력 관리와 보안 관리를 위하여 아래와 같이 개인정보를 수집·이용합니다.

1. 수집 항목: 성명, 소속, 직급, 담당업무, 연락처, 이메일, 생년월일(출입증 발급 시)
2. 이용 목적: 인력 투입·철수 관리, PC 및 계정 지급, 출입 관리, 보안 서약·교육 이력 관리
3. 보유 기간: 철수 후 1년(고객사 보안 규정에 따라 달라질 수 있음). 기간 경과 시 연락처·이메일·생년월일은 파기하며, 성명·소속·투입 이력은 감사 증빙을 위해 보관합니다.
4. 귀하는 동의를 거부할 수 있으나, 거부 시 프로젝트 투입 절차를 진행할 수 없습니다.`, 0},
	{"pledge", "보안 서약서", `본인은 프로젝트 수행 기간 및 종료 후에도 다음 사항을 준수할 것을 서약합니다.

1. 업무상 알게 된 고객사의 정보, 자료, 소스코드, 개인정보를 외부에 유출하거나 업무 외 목적으로 사용하지 않는다.
2. 지급받은 PC 외의 장비를 업무망에 연결하지 않으며, 승인 없이 저장매체를 사용하지 않는다.
3. 계정과 비밀번호를 타인과 공유하지 않으며, 자리를 비울 때 화면을 잠근다.
4. 보안 사고 또는 의심 상황을 인지하면 즉시 PM 및 보안 담당자에게 보고한다.
5. 철수 시 지급 자산과 모든 자료를 반납하고 개인 보관 사본을 삭제한다.
6. 위 사항을 위반할 경우 관련 법령 및 계약에 따른 책임을 진다.`, 0},
	{"training", "신규 투입자 보안 교육", `[교육 내용 요약]

1. 고객사 보안 정책: 업무망/인터넷망 분리, 반출입 절차, 출입 통제
2. PC 보안: 백신, 매체제어, 화면보호기, 비인가 SW 설치 금지
3. 계정 보안: 비밀번호 규칙(9자 이상, 3종 조합, 90일 변경), 공유 금지
4. 개인정보 보호: 테스트 데이터 내 개인정보 사용 금지, 마스킹 원칙
5. 사고 대응: 의심 메일·악성코드 발견 시 보고 절차와 연락처

※ 위 내용을 모두 읽고 이해하였음을 확인합니다.`, 12},
	{"exit", "철수 보안 서약서", `본인은 프로젝트 철수에 따라 다음 사항을 확인하고 서약합니다.

1. 지급받은 PC, 저장매체, 출입증 등 모든 자산을 반납하였다.
2. 업무 중 취득한 자료(문서, 소스코드, 데이터)를 개인 장비나 외부 저장소에 보관하고 있지 않으며, 보관 사본은 모두 삭제하였다.
3. 철수 이후에도 업무상 알게 된 비밀을 누설하지 않는다.`, 0},
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// 단일 연결로 쓰기 경합을 없앤다. 수십~수백 명 규모 프로젝트에는 충분하다.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	for k, v := range defaultSettings {
		if _, err := db.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES(?, ?)`, k, v); err != nil {
			return nil, err
		}
	}
	// QR 등록코드는 설치마다 무작위로 만든다(설정 화면에서 재발급 가능)
	if _, err := db.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES('join_code', ?)`, randToken(9)); err != nil {
		return nil, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		for i, d := range defaultDocs {
			if _, err := db.Exec(`INSERT INTO documents(kind, title, body, valid_months, sort, updated_at) VALUES(?, ?, ?, ?, ?, ?)`,
				d.kind, d.title, d.body, d.validMonths, i, nowTS()); err != nil {
				return nil, err
			}
		}
	}
	return db, nil
}

func nowTS() string { return time.Now().Format("2006-01-02 15:04:05") }
func today() string { return time.Now().Format("2006-01-02") }

// addDays는 YYYY-MM-DD 문자열 날짜에 일수를 더한다. 형식이 틀리면 빈 문자열.
func addDays(date string, days int) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

func addMonths(date string, months int) string {
	t, err := time.Parse("2006-01-02", firstN(date, 10))
	if err != nil {
		return ""
	}
	return t.AddDate(0, months, 0).Format("2006-01-02")
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (a *App) setting(key string) string {
	var v string
	a.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

func (a *App) settingLines(key string) []string {
	var out []string
	for _, l := range strings.Split(a.setting(key), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (a *App) settingInt(key string, def int) int {
	var n int
	if _, err := fmt.Sscanf(a.setting(key), "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}

func (a *App) audit(actor, ip, action, target, detail string) {
	a.db.Exec(`INSERT INTO audit(at, actor, action, target, detail, ip) VALUES(?, ?, ?, ?, ?, ?)`,
		nowTS(), actor, action, target, detail, ip)
}
