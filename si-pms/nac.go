package main

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// NAC·IP 자동화
//
// 고객사 NAC는 관리자가 파일을 받아 직접 등록하는 방식을 전제로 한다.
//   승인 → 장비마다 IP 자동 배정 + NAC 등록 요청(requested)
//   PMO가 등록 파일·신청서를 NAC 담당에게 전달 → 등록되면 "완료 처리"(registered)
//   철수·반납 → 해제 요청(removal_requested) → 해제 완료(removed) 시 IP 회수
// 본인 상태 페이지는 registered가 되어야 IP 정보를 보여준다.

const (
	NacRequested        = "requested"
	NacRegistered       = "registered"
	NacRemovalRequested = "removal_requested"
	NacRemoved          = "removed"
)

var nacStatusLabel = map[string]string{
	NacRequested:        "등록 대기",
	NacRegistered:       "사용 가능",
	NacRemovalRequested: "해제 대기",
	NacRemoved:          "해제 완료",
}

const byodKind = "노트북(반입)"

type NacEntry struct {
	ID                                int64
	PersonID                          int64
	AssetID                           int64
	PersonName, Company               string
	AssetNo, AssetKind, Model, Serial string
	MAC, IP, Status                   string
	StartDate, EndDate                string
	RequestedAt, RegisteredAt         string
	RemovalRequestedAt, RemovedAt     string
}

func (e *NacEntry) StatusLabel() string { return nacStatusLabel[e.Status] }

func (a *App) listNac(where string, args ...any) ([]*NacEntry, error) {
	rows, err := a.db.Query(`SELECT n.id, n.person_id, n.asset_id, p.name, p.company, s.asset_no, s.kind, s.model, s.serial,
		n.mac, n.ip, n.status, p.start_date, p.end_date, n.requested_at, n.registered_at, n.removal_requested_at, n.removed_at
		FROM nac_entries n JOIN people p ON p.id = n.person_id JOIN assets s ON s.id = n.asset_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NacEntry
	for rows.Next() {
		e := &NacEntry{}
		if err := rows.Scan(&e.ID, &e.PersonID, &e.AssetID, &e.PersonName, &e.Company, &e.AssetNo, &e.AssetKind, &e.Model, &e.Serial,
			&e.MAC, &e.IP, &e.Status, &e.StartDate, &e.EndDate, &e.RequestedAt, &e.RegisteredAt, &e.RemovalRequestedAt, &e.RemovedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

var macHexRe = regexp.MustCompile(`[^0-9A-Fa-f]`)

// normalizeMAC은 여러 표기(00-11-22..., 0011.2233..., 001122334455)를 00:11:22:33:44:55로 맞추고,
// NAC에 등록할 수 없는 주소(0, 브로드캐스트, 멀티캐스트)를 거른다.
func normalizeMAC(s string) (string, error) {
	h := strings.ToUpper(macHexRe.ReplaceAllString(s, ""))
	if len(h) != 12 {
		return "", errors.New("MAC 주소는 16진수 12자리여야 합니다(예: 00-1A-2B-3C-4D-5E)")
	}
	if h == "000000000000" || h == "FFFFFFFFFFFF" {
		return "", errors.New("사용할 수 없는 MAC 주소입니다")
	}
	first, _ := strconv.ParseUint(h[:2], 16, 8)
	if first&1 == 1 {
		return "", errors.New("멀티캐스트 MAC 주소입니다. 유선 랜카드의 '물리적 주소'를 입력해 주세요")
	}
	parts := make([]string, 6)
	for i := range parts {
		parts[i] = h[i*2 : i*2+2]
	}
	return strings.Join(parts, ":"), nil
}

// macInUse는 다른 사람이 사용 중인(철수 전) 장비에 같은 MAC이 있는지 확인한다.
func (a *App) macInUse(mac string, exceptAsset int64) bool {
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM assets WHERE mac = ? AND id != ? AND status IN ('stock', 'assigned', 'repair')`, mac, exceptAsset).Scan(&n)
	return n > 0
}

func ipToU32(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }

func u32ToIP(n uint32) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return net.IP(b).String()
}

type ipRange struct{ from, to uint32 }

// parseIPPool은 "10.0.1.10-10.0.1.200" 또는 "10.0.2.5" 형식의 줄들을 읽는다.
func parseIPPool(lines []string) ([]ipRange, error) {
	var out []ipRange
	for _, l := range lines {
		from, to, found := strings.Cut(l, "-")
		a := net.ParseIP(strings.TrimSpace(from)).To4()
		b := a
		if found {
			b = net.ParseIP(strings.TrimSpace(to)).To4()
		}
		if a == nil || b == nil || ipToU32(a) > ipToU32(b) {
			return nil, fmt.Errorf("IP 대역 형식 오류: %q (예: 10.0.1.10-10.0.1.200)", l)
		}
		out = append(out, ipRange{ipToU32(a), ipToU32(b)})
	}
	return out, nil
}

// ipPoolStatus는 설정된 대역의 전체·사용 개수를 돌려준다.
func (a *App) ipPoolStatus() (total, used int, err error) {
	pool, err := parseIPPool(a.settingLines("ip_pool"))
	if err != nil {
		return 0, 0, err
	}
	for _, r := range pool {
		total += int(r.to - r.from + 1)
	}
	inUse, err := a.ipsInUse()
	return total, len(inUse), err
}

func (a *App) ipsInUse() (map[string]bool, error) {
	rows, err := a.db.Query(`SELECT ip FROM nac_entries WHERE status != ? AND ip != ''`, NacRemoved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[string]bool{}
	for rows.Next() {
		var ip string
		rows.Scan(&ip)
		used[ip] = true
	}
	return used, rows.Err()
}

func (a *App) allocateIP() (string, error) {
	pool, err := parseIPPool(a.settingLines("ip_pool"))
	if err != nil {
		return "", err
	}
	if len(pool) == 0 {
		return "", errors.New("설정에 IP 대역이 없습니다. 설정 화면에서 IP 대역을 입력해 주세요")
	}
	used, err := a.ipsInUse()
	if err != nil {
		return "", err
	}
	// 다른 장비에 이미 수동으로 적힌 IP도 피한다
	rows, err := a.db.Query(`SELECT ip FROM assets WHERE ip != '' AND status IN ('stock', 'assigned', 'repair')`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var ip string
		rows.Scan(&ip)
		used[ip] = true
	}
	rows.Close()
	for _, r := range pool {
		for n := r.from; n <= r.to; n++ {
			if ip := u32ToIP(n); !used[ip] {
				return ip, nil
			}
		}
	}
	return "", errors.New("IP 대역이 모두 사용 중입니다. 설정에서 대역을 늘려 주세요")
}

// requestNAC는 장비 하나에 IP를 배정하고 NAC 등록 요청을 만든다. 이미 진행 중이면 그대로 둔다.
func (a *App) requestNAC(personID, assetID int64, actor string) error {
	var mac string
	if err := a.db.QueryRow(`SELECT mac FROM assets WHERE id = ?`, assetID).Scan(&mac); err != nil {
		return err
	}
	if mac == "" {
		return nil // MAC 없는 장비(모니터 등)는 네트워크 대상이 아니다
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM nac_entries WHERE asset_id = ? AND person_id = ? AND status IN (?, ?)`,
		assetID, personID, NacRequested, NacRegistered).Scan(&n)
	if n > 0 {
		return nil
	}
	ip, err := a.allocateIP()
	if err != nil {
		return err
	}
	ts := nowTS()
	if _, err := a.db.Exec(`INSERT INTO nac_entries(person_id, asset_id, mac, ip, status, requested_at) VALUES(?, ?, ?, ?, ?, ?)`,
		personID, assetID, mac, ip, NacRequested, ts); err != nil {
		return err
	}
	a.db.Exec(`UPDATE assets SET ip = ?, updated_at = ? WHERE id = ?`, ip, ts, assetID)
	a.audit(actor, "", "nac_request", fmt.Sprintf("asset:%d", assetID), mac+" "+ip)
	return nil
}

// releaseNAC는 장비의 NAC 사용을 끝낸다. 아직 등록 전이면 바로 취소하고, 등록되어 있으면 해제 요청을 만든다.
func (a *App) releaseNAC(assetID int64, actor string) {
	ts := nowTS()
	list, _ := a.listNac(`WHERE n.asset_id = ? AND n.status IN (?, ?)`, assetID, NacRequested, NacRegistered)
	for _, e := range list {
		if e.Status == NacRequested {
			a.db.Exec(`UPDATE nac_entries SET status = ?, removed_at = ? WHERE id = ?`, NacRemoved, ts, e.ID)
			a.db.Exec(`UPDATE assets SET ip = '' WHERE id = ? AND ip = ?`, assetID, e.IP)
		} else {
			a.db.Exec(`UPDATE nac_entries SET status = ?, removal_requested_at = ? WHERE id = ?`, NacRemovalRequested, ts, e.ID)
		}
		a.audit(actor, "", "nac_release", fmt.Sprintf("asset:%d", assetID), e.MAC+" "+e.IP)
	}
}

// activatePerson은 승인 처리다. 지급·반입 장비마다 IP를 배정하고 NAC 등록을 요청한다.
func (a *App) activatePerson(p *Person, approver string) error {
	pend, err := a.outstanding(p.ID, onboardKinds)
	if err != nil {
		return err
	}
	if len(pend) > 0 {
		return fmt.Errorf("%s 등 미이행 항목이 있습니다", pend[0].Title)
	}
	if p.StartDate == "" {
		return errors.New("투입일이 없습니다")
	}
	assets, err := a.listAssets(`WHERE s.person_id = ? AND s.mac != ''`, p.ID)
	if err != nil {
		return err
	}
	// IP가 모자라면 승인 자체를 막는다(일부만 배정되는 상황 방지)
	if len(assets) > 0 {
		total, used, err := a.ipPoolStatus()
		if err != nil {
			return err
		}
		if total-used < len(assets) {
			return errors.New("IP 대역이 부족합니다. 설정에서 대역을 늘려 주세요")
		}
	}
	// 승인 후에도 본인 상태 페이지(IP 확인)를 쓸 수 있도록 링크 기한을 철수예정일+30일로 늘린다.
	exp := addDays(today(), 365)
	if p.EndDate != "" {
		exp = addDays(p.EndDate, 30)
	}
	ts := nowTS()
	if _, err := a.db.Exec(`UPDATE people SET status = ?, approved_by = ?, approved_at = ?, reject_reason = '',
		token_expires = CASE WHEN token_hash != '' THEN ? ELSE token_expires END, updated_at = ? WHERE id = ?`,
		StActive, approver, ts, exp, ts, p.ID); err != nil {
		return err
	}
	for _, s := range assets {
		if err := a.requestNAC(p.ID, s.ID, approver); err != nil {
			return err
		}
	}
	return nil
}

// startOffboarding은 철수 시작 처리다. 반입 장비는 반출 처리하고 모든 NAC 사용을 해제 요청한다.
// 회사 지급 PC는 실물 반납을 PMO가 기록해야 하므로 지급 상태로 남긴다.
func (a *App) startOffboarding(p *Person, endDate, actor string) error {
	ts := nowTS()
	if _, err := a.db.Exec(`UPDATE people SET status = ?, actual_end_date = ?, updated_at = ? WHERE id = ?`, StOffboarding, endDate, ts, p.ID); err != nil {
		return err
	}
	assets, err := a.listAssets(`WHERE s.person_id = ?`, p.ID)
	if err != nil {
		return err
	}
	for _, s := range assets {
		a.releaseNAC(s.ID, actor)
		if s.Kind == byodKind {
			a.db.Exec(`UPDATE assets SET status = 'released', person_id = NULL, assigned_at = '', updated_at = ? WHERE id = ?`, ts, s.ID)
			a.db.Exec(`INSERT INTO asset_events(asset_id, person_id, person_name, kind, at, actor, note) VALUES(?, ?, ?, 'return', ?, ?, '반입 장비 반출')`,
				s.ID, p.ID, p.Name, ts, actor)
		}
	}
	return nil
}

func (a *App) handleNac(w http.ResponseWriter, r *http.Request) {
	reg, err := a.listNac(`WHERE n.status = ? ORDER BY n.requested_at`, NacRequested)
	if err != nil {
		a.fail(w, err)
		return
	}
	rem, err := a.listNac(`WHERE n.status = ? ORDER BY n.removal_requested_at`, NacRemovalRequested)
	if err != nil {
		a.fail(w, err)
		return
	}
	active, err := a.listNac(`WHERE n.status = ? ORDER BY n.ip`, NacRegistered)
	if err != nil {
		a.fail(w, err)
		return
	}
	total, used, poolErr := a.ipPoolStatus()
	d := map[string]any{"Register": reg, "Remove": rem, "Active": active, "PoolTotal": total, "PoolUsed": used,
		"Netmask": a.setting("ip_netmask"), "Gateway": a.setting("ip_gateway"), "DNS": a.setting("ip_dns")}
	if poolErr != nil {
		d["PoolErr"] = poolErr.Error()
	}
	a.render(w, r, "nac", "IP·NAC", d)
}

var nacCols = []string{"번호", "신청구분", "MAC", "IP", "성명", "소속", "자산번호", "장비", "모델", "시리얼", "사용시작", "사용종료"}

func (a *App) nacRows(kind string) ([]map[string]string, string, error) {
	status, label := NacRequested, "등록"
	if kind == "remove" {
		status, label = NacRemovalRequested, "해제"
	}
	list, err := a.listNac(`WHERE n.status = ? ORDER BY p.company, p.name`, status)
	if err != nil {
		return nil, "", err
	}
	var rows []map[string]string
	for i, e := range list {
		rows = append(rows, map[string]string{"번호": strconv.Itoa(i + 1), "신청구분": label, "MAC": e.MAC, "IP": e.IP,
			"성명": e.PersonName, "소속": e.Company, "자산번호": e.AssetNo, "장비": e.AssetKind, "모델": e.Model, "시리얼": e.Serial,
			"사용시작": e.StartDate, "사용종료": e.EndDate})
	}
	return rows, label, nil
}

func nacCSV(rows []map[string]string) []byte {
	var buf bytes.Buffer
	buf.WriteString("\ufeff")
	cw := csv.NewWriter(&buf)
	cw.Write(nacCols)
	for _, row := range rows {
		rec := make([]string, len(nacCols))
		for i, c := range nacCols {
			rec[i] = csvSafe(row[c])
		}
		cw.Write(rec)
	}
	cw.Flush()
	return buf.Bytes()
}

func (a *App) nacScalars(s map[string]string, label string, n int) map[string]string {
	s["신청구분"] = label
	s["건수"] = strconv.Itoa(n)
	s["서브넷"] = a.setting("ip_netmask")
	s["게이트웨이"] = a.setting("ip_gateway")
	s["DNS"] = a.setting("ip_dns")
	return s
}

func (a *App) handleNacExport(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	rows, label, err := a.nacRows(kind)
	if err != nil {
		a.fail(w, err)
		return
	}
	name := "NAC_" + label + "요청_" + today()
	a.audit(userFrom(r).Login, clientIP(r), "export", "nac_"+kind, r.URL.Query().Get("format"))
	if r.URL.Query().Get("format") == "csv" {
		sendFile(w, name+".csv", "text/csv; charset=utf-8", nacCSV(rows))
		return
	}
	a.sendHWPX(w, "nac_request", "IP·NAC_"+label+"_신청서_"+today(),
		FillData{Scalars: a.nacScalars(a.baseScalars(r), label, len(rows)), Lists: map[string][]map[string]string{"장비": rows}})
}

// handleNacComplete는 NAC 담당이 등록·해제를 마쳤다고 알려 오면 PMO가 한 번에 완료 처리한다.
func (a *App) handleNacComplete(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	from, to, col := NacRequested, NacRegistered, "registered_at"
	if r.FormValue("kind") == "remove" {
		from, to, col = NacRemovalRequested, NacRemoved, "removed_at"
	}
	u := userFrom(r)
	n := 0
	for _, v := range r.Form["id"] {
		id, _ := strconv.ParseInt(v, 10, 64)
		list, err := a.listNac(`WHERE n.id = ? AND n.status = ?`, id, from)
		if err != nil || len(list) == 0 {
			continue
		}
		e := list[0]
		a.db.Exec(`UPDATE nac_entries SET status = ?, `+col+` = ? WHERE id = ?`, to, nowTS(), id)
		if to == NacRemoved {
			a.db.Exec(`UPDATE assets SET ip = '' WHERE id = ? AND ip = ?`, e.AssetID, e.IP)
		}
		a.audit(u.Login, clientIP(r), "nac_"+to, fmt.Sprintf("person:%d", e.PersonID), e.MAC+" "+e.IP)
		n++
	}
	redirectMsg(w, r, "/nac", fmt.Sprintf("%d건을 %s(으)로 처리했습니다", n, nacStatusLabel[to]))
}
