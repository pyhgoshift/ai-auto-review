package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 테스트용 데이터 준비: 투입중 인원과 MAC이 있는 반입 장비를 n개 만든다.
func seedActive(t *testing.T, app *App, n int, endDate string) []int64 {
	t.Helper()
	var assetIDs []int64
	ts := nowTS()
	for i := 0; i < n; i++ {
		res, err := app.db.Exec(`INSERT INTO people(name, company, status, start_date, end_date, created_at, updated_at) VALUES(?, '협력사A', ?, '2026-11-01', ?, ?, ?)`,
			fmt.Sprintf("인원%02d", i), StActive, endDate, ts, ts)
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := res.LastInsertId()
		res, err = app.db.Exec(`INSERT INTO assets(asset_no, kind, mac, status, person_id, created_at, updated_at) VALUES(?, ?, ?, 'assigned', ?, ?, ?)`,
			fmt.Sprintf("반입-%04d", i), byodKind, fmt.Sprintf("00:1A:2B:00:00:%02X", i+1), pid, ts, ts)
		if err != nil {
			t.Fatal(err)
		}
		aid, _ := res.LastInsertId()
		assetIDs = append(assetIDs, aid)
	}
	return assetIDs
}

func setSetting(t *testing.T, app *App, key, value string) {
	t.Helper()
	if _, err := app.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		t.Fatal(err)
	}
}

func TestIPAllocationConcurrentNoDuplicates(t *testing.T) {
	app, _ := newTestApp(t)
	setSetting(t, app, "ip_pool", "10.0.1.10-10.0.1.29") // 20개
	ids := seedActive(t, app, 25, "2027-04-30")
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for i, aid := range ids {
		wg.Add(1)
		go func(pid, aid int64) {
			defer wg.Done()
			if err := app.requestNAC(pid, aid, "테스트"); err != nil {
				errs <- err
			}
		}(int64(i+1), aid)
	}
	wg.Wait()
	close(errs)
	var failed int
	for err := range errs {
		if !strings.Contains(err.Error(), "배정 가능한 IP가 없습니다") {
			t.Fatalf("예상하지 못한 오류: %v", err)
		}
		failed++
	}
	var total, distinct int
	app.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT ip) FROM nac_entries WHERE status != 'removed'`).Scan(&total, &distinct)
	if total != 20 || distinct != 20 || failed != 5 {
		t.Fatalf("배정 %d건, 서로 다른 IP %d개, 실패 %d건 (기대: 20, 20, 5)", total, distinct, failed)
	}
}

func TestIPReservedAddressesExcluded(t *testing.T) {
	app, _ := newTestApp(t)
	setSetting(t, app, "ip_pool", "10.0.1.0-10.0.1.6\n10.0.1.255")
	setSetting(t, app, "ip_netmask", "255.255.255.0")
	setSetting(t, app, "ip_gateway", "10.0.1.1")
	setSetting(t, app, "ip_dns", "10.0.1.2, 8.8.8.8")
	setSetting(t, app, "ip_reserved", "10.0.1.3\n10.0.1.5-10.0.1.5")
	total, used, err := app.ipPoolStatus()
	if err != nil || total != 2 || used != 0 {
		t.Fatalf("배정 가능 %d개, 사용 %d개, %v (기대: .4, .6 두 개)", total, used, err)
	}
	ids := seedActive(t, app, 3, "2027-04-30")
	var got []string
	for i, aid := range ids {
		err := app.requestNAC(int64(i+1), aid, "테스트")
		if i < 2 && err != nil {
			t.Fatal(err)
		}
		if i == 2 && err == nil {
			t.Fatal("예약 주소까지 배정됨")
		}
	}
	rows, _ := app.db.Query(`SELECT ip FROM nac_entries ORDER BY id`)
	for rows.Next() {
		var ip string
		rows.Scan(&ip)
		got = append(got, ip)
	}
	rows.Close()
	if strings.Join(got, ",") != "10.0.1.4,10.0.1.6" {
		t.Fatalf("배정된 IP: %v", got)
	}
}

func TestDBRejectsDuplicateActiveIP(t *testing.T) {
	app, _ := newTestApp(t)
	ids := seedActive(t, app, 2, "2027-04-30")
	ts := nowTS()
	if _, err := app.db.Exec(`INSERT INTO nac_entries(person_id, asset_id, mac, ip, status, requested_at) VALUES(1, ?, 'x', '10.9.9.9', 'registered', ?)`, ids[0], ts); err != nil {
		t.Fatal(err)
	}
	_, err := app.db.Exec(`INSERT INTO nac_entries(person_id, asset_id, mac, ip, status, requested_at) VALUES(2, ?, 'y', '10.9.9.9', 'requested', ?)`, ids[1], ts)
	if !isUniqueErr(err) {
		t.Fatalf("사용 중 IP 중복이 DB에서 막히지 않음: %v", err)
	}
	// 해제 완료된 IP는 다시 쓸 수 있다
	app.db.Exec(`UPDATE nac_entries SET status = 'removed' WHERE person_id = 1`)
	if _, err := app.db.Exec(`INSERT INTO nac_entries(person_id, asset_id, mac, ip, status, requested_at) VALUES(2, ?, 'y', '10.9.9.9', 'requested', ?)`, ids[1], ts); err != nil {
		t.Fatalf("회수된 IP 재사용 불가: %v", err)
	}
}

func TestAckDoubleSubmitRecordedOnce(t *testing.T) {
	app, srv := newTestApp(t)
	phone := newClient(t, srv)
	v := joinForm(app, "김민수", "010-1111-2222", "")
	phone.post("/join", v)
	p := app.listPeopleMust(t, `WHERE name = '김민수'`)
	docs, _ := app.listDocuments(true)
	// 같은 날 같은 문서를 서면으로 두 번 기록해도 한 건만 남는다
	for i := 0; i < 2; i++ {
		if err := app.insertAck(p, docs[0], p.Name, "offline", today()+" 00:00:00", "", "관리자"); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	app.db.QueryRow(`SELECT COUNT(*) FROM acks WHERE person_id = ? AND document_id = ?`, p.ID, docs[0].ID).Scan(&n)
	if n != 1 {
		t.Fatalf("같은 날 서명 %d건 기록됨", n)
	}
	// 다른 날(정기 재교육)은 새로 기록된다
	app.insertAck(p, docs[0], p.Name, "offline", addDays(today(), 1)+" 00:00:00", "", "관리자")
	app.db.QueryRow(`SELECT COUNT(*) FROM acks WHERE person_id = ? AND document_id = ?`, p.ID, docs[0].ID).Scan(&n)
	if n != 2 {
		t.Fatalf("다른 날 재이행이 기록되지 않음: %d건", n)
	}
}

func TestMigrationUpgradesOldDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pms.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	// 이전 버전 상태를 흉내 낸다: 제약 없음, expire_sent 열 없음, 같은 날 중복 서명
	for _, q := range []string{
		`DROP INDEX acks_once_per_day`,
		`DROP INDEX nac_ip_in_use`,
		`ALTER TABLE nac_entries DROP COLUMN expire_sent`,
		`INSERT INTO people(name, status, created_at, updated_at) VALUES('홍길동', 'active', '', '')`,
		`INSERT INTO acks(person_id, document_id, version, title, body, body_sha256, signed_name, method, acked_at) VALUES(1, 1, 1, 't', 'b', 'h', '홍길동', 'self', '2026-10-01 10:00:00')`,
		`INSERT INTO acks(person_id, document_id, version, title, body, body_sha256, signed_name, method, acked_at) VALUES(1, 1, 1, 't', 'b', 'h', '홍길동', 'self', '2026-10-01 10:00:01')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	db, err = openDB(path)
	if err != nil {
		t.Fatalf("업그레이드 실패: %v", err)
	}
	defer db.Close()
	var acks, idx, col, logged int
	db.QueryRow(`SELECT COUNT(*) FROM acks`).Scan(&acks)
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('acks_once_per_day', 'nac_ip_in_use', 'nac_asset_active')`).Scan(&idx)
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('nac_entries') WHERE name = 'expire_sent'`).Scan(&col)
	db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action = 'migrate' AND detail LIKE '%중복 서명 기록 1건%'`).Scan(&logged)
	if acks != 1 || idx != 3 || col != 1 || logged != 1 {
		t.Fatalf("업그레이드 결과: 서명 %d건, 제약 %d개, expire_sent %d, 정리 기록 %d", acks, idx, col, logged)
	}
}

func TestNACExpiryAndUpdateRequests(t *testing.T) {
	app, srv := newTestApp(t)
	admin := loginAdmin(t, app, srv)
	setSetting(t, app, "ip_pool", "10.0.1.10-10.0.1.20")
	setSetting(t, app, "nac_expire_buffer_days", "1")
	setSetting(t, app, "nac_role", "외부인력")
	setSetting(t, app, "nac_csv_columns", "MAC(하이픈)=mac\nIP=ip\n역할=role\n만료일시=unregdate")
	ids := seedActive(t, app, 1, "2027-04-30")
	if err := app.requestNAC(1, ids[0], "테스트"); err != nil {
		t.Fatal(err)
	}
	csv := string(admin.download("/nac/export?kind=register&format=csv"))
	mustContain(t, csv, "mac,ip,role,unregdate", "00-1A-2B-00-00-01,10.0.1.10,외부인력,2027-05-01 23:59:59")
	checkHWPXOutput(t, admin.download("/nac/export?kind=register"), []string{"만료일", "2027-05-01"})

	admin.get("/nac", 200)
	admin.post("/nac/complete", url.Values{"kind": {"register"}, "id": {"1"}})
	if upd, _ := app.nacPending(NacKindUpdate); len(upd) != 0 {
		t.Fatal("등록 직후 만료일 변경 대기 발생")
	}
	// 계약 연장: 철수예정일 변경 → 만료일 변경 요청 자동 생성
	app.db.Exec(`UPDATE people SET end_date = '2027-06-30' WHERE id = 1`)
	upd, _ := app.nacPending(NacKindUpdate)
	if len(upd) != 1 {
		t.Fatalf("만료일 변경 대기 %d건", len(upd))
	}
	body := admin.get("/", 200)
	mustContain(t, body, "NAC 만료일 변경 대기 1건")
	csv = string(admin.download("/nac/export?kind=update&format=csv"))
	mustContain(t, csv, "2027-07-01 23:59:59")
	admin.get("/nac", 200)
	admin.post("/nac/complete", url.Values{"kind": {"update"}, "id": {"1"}})
	if upd, _ := app.nacPending(NacKindUpdate); len(upd) != 0 {
		t.Fatal("변경 완료 후에도 대기에 남음")
	}
}

func TestSettingsValidationIsAllOrNothing(t *testing.T) {
	app, srv := newTestApp(t)
	admin := loginAdmin(t, app, srv)
	admin.get("/settings", 200)
	vals := url.Values{}
	for _, k := range settingKeys {
		vals.Set(k.Key, app.setting(k.Key))
	}
	vals.Set("project_name", "바뀌면 안 됨")
	vals.Set("nac_csv_columns", "MAC\n없는필드")
	body := admin.post("/settings", vals)
	mustContain(t, body, "저장하지 않았습니다", "없는필드")
	if app.setting("project_name") == "바뀌면 안 됨" {
		t.Fatal("검증 실패인데 다른 항목이 저장됨")
	}
	vals.Set("nac_csv_columns", "MAC")
	vals.Set("ip_gateway", "10.0.1.999")
	mustContain(t, admin.post("/settings", vals), "게이트웨이 형식")
	vals.Set("ip_gateway", "10.0.1.1")
	vals.Del("auto_complete_offboarding") // 체크 해제
	mustContain(t, admin.post("/settings", vals), "설정을 저장했습니다")
	if app.setting("project_name") != "바뀌면 안 됨" || app.setting("auto_complete_offboarding") != "0" {
		t.Fatal("정상 저장 실패")
	}
}

func TestAutoCompleteOffboarding(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("자동완료=%v", on), func(t *testing.T) {
			app, srv := newTestApp(t)
			admin := loginAdmin(t, app, srv)
			if !on {
				setSetting(t, app, "auto_complete_offboarding", "0")
			}
			ids := seedActive(t, app, 1, "2027-04-30")
			app.db.Exec(`UPDATE assets SET kind = '노트북' WHERE id = ?`, ids[0]) // 회사 지급 PC: 실물 반납 필요
			p, _ := app.getPerson(1)
			docs, _ := app.listDocuments(true)
			for _, d := range docs {
				if d.Kind == "exit" {
					app.insertAck(p, d, p.Name, "self", nowTS(), "", "본인")
				}
			}
			admin.get("/people/1", 200)
			admin.post("/people/1/offboard", url.Values{"actual_end_date": {"2027-04-30"}})
			body := admin.post(fmt.Sprintf("/assets/%d/return", ids[0]), url.Values{"back": {"/people/1"}})
			p, _ = app.getPerson(1)
			if on && (p.Status != StOffboarded || !strings.Contains(body, "철수 완료로 자동 처리")) {
				t.Fatalf("마지막 반납 후 자동 완료 안 됨: %s", p.Status)
			}
			if !on && p.Status != StOffboarding {
				t.Fatalf("설정을 껐는데 자동 완료됨: %s", p.Status)
			}
		})
	}
}

func TestJobsRecordedAndBackupRetention(t *testing.T) {
	app, srv := newTestApp(t)
	admin := loginAdmin(t, app, srv)
	setSetting(t, app, "backup_keep", "2")
	for i := 0; i < 3; i++ {
		if _, err := app.runJob(jobByKey("backup"), "테스트"); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(app.backupDir())
	if len(entries) > 2 {
		t.Fatalf("백업 %d개 남음(보관 2개)", len(entries))
	}
	// 처음 실행: 모든 작업이 due
	app.runDueJobs(false)
	var n int
	app.db.QueryRow(`SELECT COUNT(DISTINCT job) FROM job_runs WHERE ok = 1`).Scan(&n)
	if n != len(jobs) {
		t.Fatalf("성공 기록된 작업 %d개 / %d개", n, len(jobs))
	}
	// 바로 다시 돌리면 아무것도 실행되지 않는다
	var before, after int
	app.db.QueryRow(`SELECT COUNT(*) FROM job_runs`).Scan(&before)
	app.runDueJobs(false)
	app.db.QueryRow(`SELECT COUNT(*) FROM job_runs`).Scan(&after)
	if before != after {
		t.Fatalf("주기 전 재실행됨: %d → %d", before, after)
	}
	// 실패 작업: outbox 자리에 파일을 두어 패키지 생성을 실패시킨다
	os.RemoveAll(app.outboxDir())
	os.WriteFile(app.outboxDir(), []byte("x"), 0o600)
	if _, err := app.runJob(jobByKey("package"), "테스트"); err == nil {
		t.Fatal("실패해야 할 작업이 성공")
	}
	if f := app.failedJobs(); len(f) != 1 || f[0] != "제출 패키지 생성" {
		t.Fatalf("실패 작업 목록: %v", f)
	}
	mustContain(t, admin.get("/", 200), "자동 작업 실패: 제출 패키지 생성")
	body := admin.get("/jobs", 200)
	mustContain(t, body, "주간 자동 백업", "실패", "정상")
	admin.post("/jobs/run", url.Values{"job": {"cleanup"}})
	// 감사 로그는 한국어로 보이고 한국어로 검색된다
	body = admin.get("/audit?q="+url.QueryEscape("자동 작업"), 200)
	mustContain(t, body, "자동 작업 수동 실행", "만료 세션 정리")
	if strings.Contains(body, ">job_run<") {
		t.Fatal("감사 로그에 영문 작업 코드 노출")
	}
}
