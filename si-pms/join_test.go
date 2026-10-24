package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func joinForm(app *App, name, phone, mac string) url.Values {
	v := url.Values{"c": {app.setting("join_code")}, "name": {name}, "company": {"협력사A"}, "phone": {phone},
		"start_date": {"2026-11-01"}, "end_date": {"2027-04-30"}, "signature": {name}}
	if mac != "" {
		v.Set("device", "byod")
		v.Set("mac", mac)
		v.Set("serial", "SN-"+phone)
	} else {
		v.Set("device", "company")
	}
	docs, _ := app.outstandingForNew()
	for _, d := range docs {
		v.Set("agree_"+itoa(d.ID), "1")
	}
	return v
}

func postStatus(c *client, path string, v url.Values) (int, string) {
	c.t.Helper()
	res, err := c.http.PostForm(c.srv.URL+path, v)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(res.Body)
	return res.StatusCode, b.String()
}

func TestQRJoinToNetworkLifecycle(t *testing.T) {
	app, srv := newTestApp(t)
	admin := loginAdmin(t, app, srv)
	admin.get("/settings", 200)
	vals := url.Values{}
	for _, k := range settingKeys {
		vals.Set(k.Key, app.setting(k.Key))
	}
	vals.Set("ip_pool", "10.0.1.10-10.0.1.11")
	vals.Set("ip_gateway", "10.0.1.1")
	vals.Set("allowed_companies", "협력사A\n협력사B")
	admin.post("/settings", vals)
	admin.get("/qr", 200)
	admin.download("/qr.png")

	// 잘못된 등록코드
	phone := newClient(t, srv)
	phone.get("/join?c=wrong", 404)
	phone.get("/join?c="+app.setting("join_code"), 200)

	// 잘못된 MAC, 허용되지 않은 소속사는 거절
	v := joinForm(app, "김민수", "010-1111-2222", "01:00:5E:00:00:01")
	if code, body := postStatus(phone, "/join", v); code != 400 || !strings.Contains(body, "멀티캐스트") {
		t.Fatalf("멀티캐스트 MAC 통과: %d", code)
	}
	v = joinForm(app, "김민수", "010-1111-2222", "00-1a-2b-3c-4d-5e")
	v.Set("company", "아무회사")
	if code, _ := postStatus(phone, "/join", v); code != 400 {
		t.Fatal("허용되지 않은 소속사 통과")
	}

	// 정상 등록 → 상태 페이지(승인 대기)
	body := phone.post("/join", joinForm(app, "김민수", "010-1111-2222", "00-1a-2b-3c-4d-5e"))
	mustContain(t, body, "등록이 접수되었습니다", "대기 중")
	// 같은 휴대폰으로 QR 재스캔 → 상태 페이지로
	body = phone.get("/join?c="+app.setting("join_code"), 200)
	mustContain(t, body, "진행 상태")

	// 중복 등록, 같은 MAC 다른 사람 거절
	other := newClient(t, srv)
	if code, _ := postStatus(other, "/join", joinForm(app, "김민수", "010-1111-2222", "")); code != 400 {
		t.Fatal("중복 등록 통과")
	}
	if code, body := postStatus(other, "/join", joinForm(app, "이영희", "010-3333-4444", "001A2B3C4D5E")); code != 400 || !strings.Contains(body, "이미 다른 장비") {
		t.Fatal("MAC 중복 통과")
	}
	other.post("/join", joinForm(app, "이영희", "010-3333-4444", "00:1A:2B:3C:4D:99"))
	third := newClient(t, srv)
	third.post("/join", joinForm(app, "박철수", "010-5555-6666", "00:1A:2B:3C:4D:77"))

	// 대시보드 일괄 승인: IP가 2개뿐이므로 2명만 승인
	body = admin.get("/", 200)
	mustContain(t, body, "승인 대기 3명", "일괄 승인")
	admin.post("/people/approve-ready", nil)
	var active int
	app.db.QueryRow(`SELECT COUNT(*) FROM people WHERE status = ?`, StActive).Scan(&active)
	if active != 2 {
		t.Fatalf("IP 2개 대역에서 %d명 승인됨", active)
	}
	body = admin.get("/", 200)
	mustContain(t, body, "IP 여유")

	// 승인됐지만 NAC 등록 전에는 IP를 보여주지 않는다
	body = phone.get("/join?c="+app.setting("join_code"), 200)
	mustContain(t, body, "NAC 등록이 끝나면")
	if strings.Contains(body, "10.0.1.1") && strings.Contains(body, "<b>10.0.1.10</b>") {
		t.Fatal("NAC 등록 전에 IP 노출")
	}

	// NAC 등록 파일 → 완료 처리 → IP 공개
	csv := string(admin.download("/nac/export?kind=register&format=csv"))
	mustContain(t, csv, "00:1A:2B:3C:4D:5E", "10.0.1.10", "10.0.1.11")
	checkHWPXOutput(t, admin.download("/nac/export?kind=register"), []string{"IP·NAC 등록 신청서", "00:1A:2B:3C:4D:5E", "10.0.1.10"})
	list, _ := app.listNac(`WHERE n.status = ?`, NacRequested)
	form := url.Values{"kind": {"register"}}
	for _, e := range list {
		form.Add("id", itoa(e.ID))
	}
	admin.get("/nac", 200)
	admin.post("/nac/complete", form)
	body = phone.get("/join?c="+app.setting("join_code"), 200)
	mustContain(t, body, "<b>10.0.1.10</b>", "10.0.1.1", "사용 가능")

	// 철수예정일 경과 → 자동 철수: 반입 장비 반출, NAC 해제 요청
	app.db.Exec(`UPDATE people SET end_date = '2026-01-01' WHERE name = '김민수'`)
	app.runDueJobs(true)
	p := app.listPeopleMust(t, `WHERE name = '김민수'`)
	if p.Status != StOffboarding {
		t.Fatal("자동 철수 안 됨", p.Status)
	}
	rem, _ := app.listNac(`WHERE n.status = ?`, NacRemovalRequested)
	if len(rem) != 1 || rem[0].IP != "10.0.1.10" {
		t.Fatalf("해제 요청 이상: %+v", rem)
	}
	// 해제 완료 → IP 회수 → 남은 1명 승인 가능
	admin.get("/nac", 200)
	admin.post("/nac/complete", url.Values{"kind": {"remove"}, "id": {itoa(rem[0].ID)}})
	admin.get("/", 200)
	admin.post("/people/approve-ready", nil)
	n, _ := app.listNac(`WHERE p.name = '박철수'`)
	if len(n) != 1 || n[0].IP != "10.0.1.10" {
		t.Fatalf("회수된 IP 재배정 안 됨: %+v", n)
	}

	// 철수 체크리스트: 철수 서약만 남음
	items, ready, _ := app.exitChecklist(p)
	if ready {
		t.Fatal("철수 서약 없이 완료 가능")
	}
	for _, it := range items {
		if !it.Done && it.Label != "철수 서약" {
			t.Fatalf("미완료 항목: %s", it.Label)
		}
	}

	// 제출 패키지
	pkg := admin.download("/package")
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"대장/인력 투입 관리 대장.hwpx", "대장/PC·자산 관리 대장.csv", "NAC/NAC_등록요청.csv", "NAC/IP_사용현황.csv", "목록.txt"} {
		if !names[want] {
			t.Fatalf("패키지에 %s 없음: %v", want, names)
		}
	}
	if len(app.outboxFiles()) == 0 {
		t.Fatal("outbox 자동 생성 안 됨")
	}
	admin.get("/exports", 200)
	admin.download("/outbox/" + app.outboxFiles()[0])
	admin.get("/people/1", 200)
	admin.get("/assets?status=released", 200)
	if res, _ := http.Get(srv.URL + "/package"); res.Request.URL.Path != "/login" {
		t.Fatal("비로그인 패키지 접근")
	}
}

func (a *App) listPeopleMust(t *testing.T, where string) *Person {
	t.Helper()
	list, err := a.listPeople(where)
	if err != nil || len(list) != 1 {
		t.Fatalf("person query %s: %v %d", where, err, len(list))
	}
	return list[0]
}

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"00-1a-2b-3c-4d-5e": "00:1A:2B:3C:4D:5E",
		"001A.2B3C.4D5E":    "00:1A:2B:3C:4D:5E",
		"001a2b3c4d5e":      "00:1A:2B:3C:4D:5E",
	} {
		if got, err := normalizeMAC(in); err != nil || got != want {
			t.Errorf("%s → %s, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "00:00:00:00:00:00", "FF-FF-FF-FF-FF-FF", "01:00:5E:00:00:01", "12:34"} {
		if _, err := normalizeMAC(bad); err == nil {
			t.Errorf("%q 통과", bad)
		}
	}
}
