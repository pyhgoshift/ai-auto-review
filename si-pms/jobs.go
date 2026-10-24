package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 자동 작업
//
// PMO가 한 명뿐이므로 사람이 누르지 않아도 정해진 주기에 아래 작업이 돈다.
// 작업마다 실행 기록(job_runs)을 남겨, 어느 작업이 언제 실패했는지 화면에서 바로 확인한다.

type Job struct {
	Key   string
	Name  string
	Desc  string
	Every time.Duration // 마지막 성공 후 이 시간이 지나면 다시 실행
	Daily bool          // true면 날짜가 바뀔 때마다 실행(Every 무시)
	Run   func(a *App) (string, error)
}

var jobs = []*Job{
	{Key: "auto_offboard", Name: "철수예정일 경과 자동 철수", Daily: true,
		Desc: "철수예정일이 지난 투입 인원을 철수 진행으로 바꾸고, 반입 장비 반출과 NAC 해제 요청을 만듭니다.",
		Run:  (*App).jobAutoOffboard},
	{Key: "auto_complete", Name: "철수 자동 완료", Every: time.Hour,
		Desc: "철수 체크리스트(자산·계정·출입증·NAC·철수서약)를 모두 채운 인원을 철수 완료로 바꿉니다.",
		Run:  (*App).jobAutoComplete},
	{Key: "package", Name: "제출 패키지 생성", Daily: true,
		Desc: "대장 전체와 NAC 신청서·IP 사용현황을 ZIP으로 만들어 outbox 폴더에 보관합니다(최근 30일).",
		Run:  (*App).jobPackage},
	{Key: "backup", Name: "주간 자동 백업", Every: 7 * 24 * time.Hour,
		Desc: "DB 전체를 backup 폴더에 복사하고, 설정한 개수보다 오래된 백업은 지웁니다.",
		Run:  (*App).jobBackup},
	{Key: "cleanup", Name: "만료 세션 정리", Daily: true,
		Desc: "만료된 로그인 세션을 지웁니다.",
		Run:  (*App).jobCleanup},
}

func jobByKey(key string) *Job {
	for _, j := range jobs {
		if j.Key == key {
			return j
		}
	}
	return nil
}

type JobRun struct {
	Job, StartedAt, FinishedAt, Message, Actor string
	OK                                         bool
}

func (a *App) lastRun(key string, okOnly bool) *JobRun {
	q := `SELECT job, started_at, finished_at, ok, message, actor FROM job_runs WHERE job = ?`
	if okOnly {
		q += ` AND ok = 1`
	}
	r := &JobRun{}
	if err := a.db.QueryRow(q+` ORDER BY id DESC LIMIT 1`, key).Scan(&r.Job, &r.StartedAt, &r.FinishedAt, &r.OK, &r.Message, &r.Actor); err != nil {
		return nil
	}
	return r
}

// due는 작업을 지금 실행해야 하는지 판단한다. 실패한 작업은 다음 점검 주기에 다시 시도한다.
func (a *App) due(j *Job, now time.Time) bool {
	last := a.lastRun(j.Key, true)
	if last == nil {
		return true
	}
	if j.Daily {
		return firstN(last.StartedAt, 10) != now.Format("2006-01-02")
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", last.StartedAt, time.Local)
	return err != nil || now.Sub(t) >= j.Every
}

func (a *App) runJob(j *Job, actor string) (string, error) {
	a.jobMu.Lock()
	defer a.jobMu.Unlock()
	res, err := a.db.Exec(`INSERT INTO job_runs(job, started_at, actor) VALUES(?, ?, ?)`, j.Key, nowTS(), actor)
	if err != nil {
		return "", err
	}
	id, _ := res.LastInsertId()
	msg, runErr := func() (msg string, err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("작업 중 예기치 않은 오류: %v", p)
			}
		}()
		return j.Run(a)
	}()
	ok := runErr == nil
	if runErr != nil {
		msg = "실패: " + runErr.Error()
		log.Printf("자동 작업 [%s] 실패: %v", j.Name, runErr)
	}
	a.db.Exec(`UPDATE job_runs SET finished_at = ?, ok = ?, message = ? WHERE id = ?`, nowTS(), ok, msg, id)
	a.db.Exec(`DELETE FROM job_runs WHERE job = ? AND id NOT IN (SELECT id FROM job_runs WHERE job = ? ORDER BY id DESC LIMIT 200)`, j.Key, j.Key)
	return msg, runErr
}

// runDueJobs는 실행할 때가 된 작업을 모두 돌린다. force면 주기와 관계없이 모두 실행한다.
func (a *App) runDueJobs(force bool) {
	now := time.Now()
	for _, j := range jobs {
		if force || a.due(j, now) {
			a.runJob(j, "자동")
		}
	}
}

func (a *App) startScheduler() {
	go func() {
		for {
			a.runDueJobs(false)
			time.Sleep(10 * time.Minute)
		}
	}()
}

func (a *App) jobAutoOffboard() (string, error) {
	people, err := a.listPeople(`WHERE status = ? AND end_date != '' AND end_date < ?`, StActive, today())
	if err != nil {
		return "", err
	}
	var names []string
	for _, p := range people {
		if err := a.startOffboarding(p, p.EndDate, "자동"); err != nil {
			return "", err
		}
		a.audit("자동", "", "person_offboard", fmt.Sprintf("person:%d", p.ID), p.Name+" 철수예정일 경과")
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return "대상 없음", nil
	}
	return fmt.Sprintf("%d명 철수 진행: %s", len(names), strings.Join(names, ", ")), nil
}

// tryCompleteOffboarding은 체크리스트가 모두 채워졌으면 철수 완료로 바꾼다.
// 자산 반납·계정 회수·NAC 해제 완료 등 체크리스트에 영향을 주는 처리 직후에 부른다.
func (a *App) tryCompleteOffboarding(personID int64, actor string) bool {
	if a.setting("auto_complete_offboarding") != "1" {
		return false
	}
	p, err := a.getPerson(personID)
	if err != nil || p.Status != StOffboarding {
		return false
	}
	_, ready, err := a.exitChecklist(p)
	if err != nil || !ready {
		return false
	}
	res, err := a.db.Exec(`UPDATE people SET status = ?, token_hash = '', token_expires = '', updated_at = ? WHERE id = ? AND status = ?`,
		StOffboarded, nowTS(), p.ID, StOffboarding)
	if err != nil {
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false
	}
	a.audit(actor, "", "person_complete", fmt.Sprintf("person:%d", p.ID), p.Name+" 체크리스트 완료로 자동 처리")
	return true
}

func (a *App) jobAutoComplete() (string, error) {
	if a.setting("auto_complete_offboarding") != "1" {
		return "설정에서 꺼져 있음", nil
	}
	people, err := a.listPeople(`WHERE status = ?`, StOffboarding)
	if err != nil {
		return "", err
	}
	var names []string
	for _, p := range people {
		if a.tryCompleteOffboarding(p.ID, "자동") {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("철수 진행 %d명 중 완료 대상 없음", len(people)), nil
	}
	return fmt.Sprintf("%d명 철수 완료: %s", len(names), strings.Join(names, ", ")), nil
}

func (a *App) jobPackage() (string, error) {
	path, err := a.writeOutbox()
	if err != nil {
		return "", err
	}
	// 30일보다 오래된 패키지는 지운다
	cut := "제출패키지_" + addDays(today(), -30)
	entries, _ := os.ReadDir(a.outboxDir())
	removed := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "제출패키지_") && e.Name() < cut {
			if os.Remove(filepath.Join(a.outboxDir(), e.Name())) == nil {
				removed++
			}
		}
	}
	msg := filepath.Base(path) + " 생성"
	if removed > 0 {
		msg += fmt.Sprintf(", 오래된 패키지 %d개 삭제", removed)
	}
	return msg, nil
}

func (a *App) backupDir() string { return filepath.Join(a.dataDir, "backup") }

// makeBackup은 실행 중에도 일관된 DB 사본을 만든다.
func (a *App) makeBackup() (string, error) {
	os.MkdirAll(a.backupDir(), 0o700)
	base := "백업_" + strings.ReplaceAll(strings.ReplaceAll(nowTS(), ":", ""), " ", "_")
	path := filepath.Join(a.backupDir(), base+".db")
	// 같은 초에 두 번 백업하면(자동·수동 겹침) 이름 뒤에 번호를 붙인다
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(a.backupDir(), fmt.Sprintf("%s_%d.db", base, i))
	}
	if _, err := a.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) jobBackup() (string, error) {
	path, err := a.makeBackup()
	if err != nil {
		return "", err
	}
	keep := a.settingInt("backup_keep", 8)
	entries, _ := os.ReadDir(a.backupDir())
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	removed := 0
	for len(names) > keep {
		if os.Remove(filepath.Join(a.backupDir(), names[0])) == nil {
			removed++
		}
		names = names[1:]
	}
	info, _ := os.Stat(path)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return fmt.Sprintf("%s (%.1fMB), 보관 %d개, 삭제 %d개", filepath.Base(path), float64(size)/1e6, len(names), removed), nil
}

func (a *App) jobCleanup() (string, error) {
	res, err := a.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, nowTS())
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	return fmt.Sprintf("세션 %d개 정리", n), nil
}

// failedJobs는 마지막 실행이 실패한 작업 이름 목록이다(대시보드 알림용).
func (a *App) failedJobs() []string {
	var out []string
	for _, j := range jobs {
		if last := a.lastRun(j.Key, false); last != nil && !last.OK && last.FinishedAt != "" {
			out = append(out, j.Name)
		}
	}
	return out
}

type jobView struct {
	*Job
	Last, LastOK *JobRun
	Every        string
	History      []JobRun
}

func (a *App) handleJobs(w http.ResponseWriter, r *http.Request) {
	var list []jobView
	for _, j := range jobs {
		v := jobView{Job: j, Last: a.lastRun(j.Key, false), LastOK: a.lastRun(j.Key, true)}
		switch {
		case j.Daily:
			v.Every = "매일"
		case j.Every >= 24*time.Hour:
			v.Every = fmt.Sprintf("%d일마다", int(j.Every.Hours()/24))
		default:
			v.Every = fmt.Sprintf("%d시간마다", int(j.Every.Hours()))
		}
		rows, err := a.db.Query(`SELECT job, started_at, finished_at, ok, message, actor FROM job_runs WHERE job = ? ORDER BY id DESC LIMIT 5`, j.Key)
		if err != nil {
			a.fail(w, err)
			return
		}
		for rows.Next() {
			var h JobRun
			rows.Scan(&h.Job, &h.StartedAt, &h.FinishedAt, &h.OK, &h.Message, &h.Actor)
			v.History = append(v.History, h)
		}
		rows.Close()
		list = append(list, v)
	}
	a.render(w, r, "jobs", "자동 작업", map[string]any{"Jobs": list})
}

func (a *App) handleJobRun(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	key := r.FormValue("job")
	if key == "all" {
		for _, j := range jobs {
			a.runJob(j, u.Name)
		}
		a.audit(u.Login, clientIP(r), "job_run", "all", "")
		redirectMsg(w, r, "/jobs", "모든 자동 작업을 지금 실행했습니다")
		return
	}
	j := jobByKey(key)
	if j == nil {
		http.NotFound(w, r)
		return
	}
	msg, err := a.runJob(j, u.Name)
	a.audit(u.Login, clientIP(r), "job_run", j.Key, msg)
	if err != nil {
		redirectMsg(w, r, "/jobs", j.Name+" "+msg)
		return
	}
	redirectMsg(w, r, "/jobs", j.Name+" 완료: "+msg)
}
