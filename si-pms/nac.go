package main

import (
	"errors"
	"fmt"
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
	Hostname, ExpireSent              string
}

func (e *NacEntry) StatusLabel() string { return nacStatusLabel[e.Status] }

func (a *App) listNac(where string, args ...any) ([]*NacEntry, error) {
	rows, err := a.db.Query(`SELECT n.id, n.person_id, n.asset_id, p.name, p.company, s.asset_no, s.kind, s.model, s.serial,
		n.mac, n.ip, n.status, p.start_date, p.end_date, n.requested_at, n.registered_at, n.removal_requested_at, n.removed_at,
		s.hostname, n.expire_sent
		FROM nac_entries n JOIN people p ON p.id = n.person_id JOIN assets s ON s.id = n.asset_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NacEntry
	for rows.Next() {
		e := &NacEntry{}
		if err := rows.Scan(&e.ID, &e.PersonID, &e.AssetID, &e.PersonName, &e.Company, &e.AssetNo, &e.AssetKind, &e.Model, &e.Serial,
			&e.MAC, &e.IP, &e.Status, &e.StartDate, &e.EndDate, &e.RequestedAt, &e.RegisteredAt, &e.RemovalRequestedAt, &e.RemovedAt,
			&e.Hostname, &e.ExpireSent); err != nil {
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
