package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type client struct {
	t    *testing.T
	srv  *httptest.Server
	http *http.Client
	csrf string
}

func newTestApp(t *testing.T) (*App, *httptest.Server) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "pms.db"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, dataDir: dir}
	app.loadTemplates()
	srv := httptest.NewServer(app.routes())
	t.Cleanup(func() { srv.Close(); db.Close() })
	return app, srv
}

func newClient(t *testing.T, srv *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, srv: srv, http: &http.Client{Jar: jar}}
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (c *client) get(path string, want int) string {
	c.t.Helper()
	res, err := c.http.Get(c.srv.URL + path)
	if err != nil {
		c.t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != want {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, res.StatusCode, want, b)
	}
	if m := csrfRe.FindSubmatch(b); m != nil {
		c.csrf = string(m[1])
	}
	return string(b)
}

func (c *client) post(path string, vals url.Values) string {
	c.t.Helper()
	if vals == nil {
		vals = url.Values{}
	}
	if c.csrf != "" {
		vals.Set("csrf", c.csrf)
	}
	res, err := c.http.PostForm(c.srv.URL+path, vals)
	if err != nil {
		c.t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode >= 400 {
		c.t.Fatalf("POST %s: status %d\n%s", path, res.StatusCode, b)
	}
	return string(b)
}

func (c *client) download(path string) []byte {
	c.t.Helper()
	res, err := c.http.Get(c.srv.URL + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		c.t.Fatalf("GET %s: %d %s", path, res.StatusCode, b)
	}
	return b
}

func loginAdmin(t *testing.T, app *App, srv *httptest.Server) *client {
	pw, err := app.ensureAdmin()
	if err != nil || pw == "" {
		t.Fatal("admin not created", err)
	}
	c := newClient(t, srv)
	c.post("/login", url.Values{"login": {"admin"}, "password": {pw}})
	body := c.get("/password", 200)
	if !strings.Contains(body, "임시 비밀번호") {
		t.Fatal("must-change notice missing")
	}
	c.post("/password", url.Values{"current": {pw}, "new": {"Abcdef1234!"}, "confirm": {"Abcdef1234!"}})
	c.get("/", 200)
	return c
}

func mustContain(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Fatalf("응답에 %q 없음", s)
		}
	}
}

var linkRe = regexp.MustCompile(`value="(http://[^"]+/s/[^"]+)"`)

func TestFullLifecycle(t *testing.T) {
	app, srv := newTestApp(t)
	c := loginAdmin(t, app, srv)

	// 1. 투입 요청 → 링크 발급
	c.get("/people/new", 200)
	body := c.post("/people/new", url.Values{"name": {"홍길동"}, "company": {"협력사A"}, "duty": {"백엔드 개발"},
		"start_date": {"2026-11-01"}, "end_date": {"2027-04-30"}, "badge_no": {"B-101"}})
	m := linkRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("링크가 표시되지 않음")
	}
	selfPath := strings.TrimPrefix(m[1], srv.URL)

	// 승인 시도 → 미이행으로 거절
	body = c.post("/people/1/approve", nil)
	mustContain(t, body, "승인 불가")

	// 2. 본인 입력 (별도 브라우저, 로그인 없음)
	self := newClient(t, srv)
	body = self.get(selfPath, 200)
	mustContain(t, body, "개인정보 수집·이용 동의서", "보안 서약서", "신규 투입자 보안 교육")
	docs, _ := app.listDocuments(true)
	agree := url.Values{"duty": {"백엔드 개발"}, "position": {"선임"}, "phone": {"010-1234-5678"}, "email": {"hong@example.com"}, "signature": {"홍 길동"}}
	for _, d := range docs {
		if d.Kind != "exit" {
			agree.Set("agree_"+itoa(d.ID), "1")
		}
	}
	// 서명 이름 불일치는 거절
	bad := url.Values{}
	for k, v := range agree {
		bad[k] = v
	}
	bad.Set("signature", "김철수")
	res, _ := self.http.PostForm(srv.URL+selfPath, bad)
	if res.StatusCode != 400 {
		t.Fatalf("잘못된 서명이 통과됨: %d", res.StatusCode)
	}
	body = self.post(selfPath, agree)
	mustContain(t, body, "제출되었습니다")

	p, _ := app.getPerson(1)
	if p.Status != StSubmitted || p.Phone != "010-1234-5678" {
		t.Fatalf("submitted 상태가 아님: %+v", p)
	}

	// 3. 승인 → 링크 무효화
	c.get("/people/1", 200)
	c.post("/people/1/approve", nil)
	p, _ = app.getPerson(1)
	if p.Status != StActive {
		t.Fatal("승인 실패", p.Status)
	}
	self.get(selfPath, 200) // 승인 후에도 상태 페이지(IP 확인)는 유지

	// 4. 자산 등록·지급·점검, 계정 발급
	c.get("/assets/new", 200)
	c.post("/assets/new", url.Values{"asset_no": {"PC-001"}, "kind": {"데스크톱"}, "model": {"OptiPlex"}, "serial": {"SN1"}})
	c.post("/assets/new", url.Values{"asset_no": {"MON-001"}, "kind": {"모니터"}})
	c.post("/people/1/assign", url.Values{"asset_id": {"1"}})
	c.post("/assets/2/assign", url.Values{"person_id": {"1"}})
	items := app.settingLines("check_items")
	c.post("/assets/1/check", url.Values{"ok": items[:2]}) // 일부만 체크 → 부적합
	body = c.get("/", 200)
	mustContain(t, body, "최근 보안 점검 부적합", "MON-001")
	c.post("/assets/1/check", url.Values{"ok": items})
	c.post("/people/1/accounts", url.Values{"system": {"VPN"}, "login_id": {"hong"}})
	c.post("/people/1/accounts", url.Values{"system": {"형상관리(Git)"}, "login_id": {"hong.gd"}})

	// 모든 조회 화면 렌더링 확인
	for _, path := range []string{"/", "/people", "/people?status=active", "/people/1", "/assets", "/assets/1", "/docs", "/docs/1", "/docs/new", "/exports", "/settings", "/audit"} {
		c.get(path, 200)
	}

	// 5. 철수
	c.post("/people/1/offboard", url.Values{"actual_end_date": {"2027-04-30"}})
	body = c.post("/people/1/complete", nil)
	mustContain(t, body, "철수 완료 불가")
	body = c.post("/people/1/link", nil)
	m = linkRe.FindStringSubmatch(body)
	exitPath := strings.TrimPrefix(m[1], srv.URL)
	body = self.get(exitPath, 200)
	mustContain(t, body, "철수 보안 서약서")
	var exitID int64
	for _, d := range docs {
		if d.Kind == "exit" {
			exitID = d.ID
		}
	}
	self.post(exitPath, url.Values{"signature": {"홍길동"}, "agree_" + itoa(exitID): {"1"}})
	c.post("/assets/1/return", url.Values{"note": {"포맷 완료"}})
	c.post("/assets/2/return", nil)
	c.post("/accounts/1/revoke", nil)
	c.post("/accounts/2/revoke", nil)
	c.post("/people/1/badge-return", nil)
	c.post("/people/1/complete", nil)
	p, _ = app.getPerson(1)
	if p.Status != StOffboarded {
		t.Fatal("철수 완료 안 됨", p.Status)
	}

	// 6. 출력물
	for _, l := range ledgers {
		if l.PerRecord {
			continue
		}
		checkHWPXOutput(t, c.download("/exports/"+l.Key), nil)
		csv := string(c.download("/exports/" + l.Key + "?format=csv"))
		if !strings.HasPrefix(csv, "\ufeff") {
			t.Fatal("CSV BOM 없음")
		}
	}
	checkHWPXOutput(t, c.download("/exports/people"), []string{"홍길동", "협력사A", "백엔드 개발"})
	checkHWPXOutput(t, c.download("/exports/assets"), []string{"PC-001", "MON-001"})
	exit := c.download("/people/1/exit.hwpx")
	checkHWPXOutput(t, exit, []string{"홍길동", "PC-001", "MON-001", "VPN", "hong.gd", "철수 보안 서약서", "2027-04-30"})
	checkHWPXOutput(t, c.download("/acks/2/hwpx"), []string{"보안 서약서", "홍 길동"})
	if os.Getenv("HWPX_OUT") != "" {
		os.WriteFile(filepath.Join(os.Getenv("HWPX_OUT"), "exit.hwpx"), exit, 0o644)
		os.WriteFile(filepath.Join(os.Getenv("HWPX_OUT"), "people.hwpx"), c.download("/exports/people"), 0o644)
		os.WriteFile(filepath.Join(os.Getenv("HWPX_OUT"), "pledge.hwpx"), c.download("/acks/2/hwpx"), 0o644)
	}

	// 7. 개인정보 파기
	c.post("/people/1/purge", nil)
	p, _ = app.getPerson(1)
	if p.Phone != "" || p.Email != "" || p.PurgedAt == "" || p.Name != "홍길동" {
		t.Fatalf("파기 결과 이상: %+v", p)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// checkHWPXOutput은 출력물이 HWPX 규칙(맨 앞 무압축 mimetype)을 지키고, XML이 올바르며,
// 남은 자리표시자가 없고, 표의 rowCnt가 실제 행 수와 같은지 확인한다.
func checkHWPXOutput(t *testing.T, b []byte, want []string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Fatal("mimetype이 맨 앞 무압축이 아님")
	}
	var text strings.Builder
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		data, _ := readZipFile(f)
		dec := xml.NewDecoder(bytes.NewReader(data))
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: XML 오류 %v", f.Name, err)
			}
			if cd, ok := tok.(xml.CharData); ok {
				text.Write(cd)
			}
		}
		if strings.HasPrefix(f.Name, "Contents/section") {
			s := string(data)
			for _, sp := range topLevel(s, "hp:tbl") {
				tbl := s[sp[0]:sp[1]]
				body := tbl[strings.IndexByte(tbl, '>')+1:]
				n := len(topLevel(body, "hp:tr"))
				if !strings.Contains(tbl[:strings.IndexByte(tbl, '>')], `rowCnt="`+itoa(int64(n))+`"`) {
					t.Fatalf("rowCnt 불일치: 실제 %d행", n)
				}
			}
		}
	}
	all := text.String()
	if strings.Contains(all, "{{") {
		i := strings.Index(all, "{{")
		t.Fatalf("채워지지 않은 자리표시자: %s", all[i:min(len(all), i+40)])
	}
	mustContain(t, all, want...)
}

func TestViewerCannotWrite(t *testing.T) {
	app, srv := newTestApp(t)
	c := loginAdmin(t, app, srv)
	body := c.post("/users", url.Values{"login": {"viewer"}, "name": {"고객보안"}, "role": {"viewer"}})
	m := regexp.MustCompile(`임시 비밀번호: (\S+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("임시 비밀번호 없음")
	}
	v := newClient(t, srv)
	v.post("/login", url.Values{"login": {"viewer"}, "password": {m[1]}})
	v.get("/password", 200)
	v.post("/password", url.Values{"current": {m[1]}, "new": {"Viewer12345!"}, "confirm": {"Viewer12345!"}})
	v.get("/people", 200)
	v.get("/exports", 200)
	v.get("/settings", 403)
	res, _ := v.http.PostForm(srv.URL+"/people/new", url.Values{"csrf": {v.csrf}, "name": {"x"}, "company": {"y"}})
	if res.StatusCode != 403 {
		t.Fatalf("viewer 쓰기 허용됨: %d", res.StatusCode)
	}
}

func TestCSRFAndLockout(t *testing.T) {
	app, srv := newTestApp(t)
	c := loginAdmin(t, app, srv)
	res, _ := c.http.PostForm(srv.URL+"/people/new", url.Values{"name": {"x"}, "company": {"y"}})
	if res.StatusCode != 403 {
		t.Fatalf("CSRF 토큰 없이 통과: %d", res.StatusCode)
	}
	x := newClient(t, srv)
	for i := 0; i < maxFailedLogin; i++ {
		res, _ := x.http.PostForm(srv.URL+"/login", url.Values{"login": {"admin"}, "password": {"wrong"}})
		res.Body.Close()
	}
	_, err := app.login("admin", "Abcdef1234!")
	if err == nil || !strings.Contains(err.Error(), "잠겼") {
		t.Fatal("잠금 미동작", err)
	}
}
