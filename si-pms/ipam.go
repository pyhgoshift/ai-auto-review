package main

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// IP 대역 관리
//
// 배정에서 빼는 주소: 서브넷의 네트워크·브로드캐스트 주소, 게이트웨이, DNS, 설정의 "예약 IP".
// 빈 IP를 고르고 기록하는 과정은 잠금 + 트랜잭션 안에서 한 번에 처리하고,
// DB의 유일성 제약(nac_ip_in_use)이 마지막 안전장치 역할을 한다.

const maxPoolSize = 65536

func ipToU32(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }

func u32ToIP(n uint32) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return net.IP(b).String()
}

type ipRange struct{ from, to uint32 }

// parseIPRanges는 "10.0.1.10-10.0.1.200" 또는 "10.0.2.5" 형식의 줄들을 읽는다.
func parseIPRanges(lines []string) ([]ipRange, error) {
	var out []ipRange
	for _, l := range lines {
		from, to, found := strings.Cut(l, "-")
		a := net.ParseIP(strings.TrimSpace(from)).To4()
		b := a
		if found {
			b = net.ParseIP(strings.TrimSpace(to)).To4()
		}
		if a == nil || b == nil || ipToU32(a) > ipToU32(b) {
			return nil, fmt.Errorf("IP 형식 오류: %q (예: 10.0.1.10-10.0.1.200 또는 10.0.1.5)", l)
		}
		out = append(out, ipRange{ipToU32(a), ipToU32(b)})
	}
	return out, nil
}

// parseIPPool은 설정 화면 검증에서도 쓴다.
func parseIPPool(lines []string) ([]ipRange, error) {
	ranges, err := parseIPRanges(lines)
	if err != nil {
		return nil, err
	}
	size := 0
	for _, r := range ranges {
		size += int(r.to-r.from) + 1
		if size > maxPoolSize {
			return nil, fmt.Errorf("IP 대역이 너무 큽니다(최대 %d개)", maxPoolSize)
		}
	}
	return ranges, nil
}

type ipPool struct {
	ranges   []ipRange
	reserved map[uint32]string // 주소 → 예약 사유
	mask     uint32
}

func (a *App) loadIPPool() (*ipPool, error) {
	ranges, err := parseIPPool(a.settingLines("ip_pool"))
	if err != nil {
		return nil, err
	}
	p := &ipPool{ranges: ranges, reserved: map[uint32]string{}}
	if m := net.ParseIP(a.setting("ip_netmask")).To4(); m != nil {
		p.mask = ipToU32(m)
	}
	addOne := func(s, why string) {
		if ip := net.ParseIP(strings.TrimSpace(s)).To4(); ip != nil {
			p.reserved[ipToU32(ip)] = why
		}
	}
	addOne(a.setting("ip_gateway"), "게이트웨이")
	for _, d := range strings.Split(a.setting("ip_dns"), ",") {
		addOne(d, "DNS")
	}
	extra, err := parseIPRanges(a.settingLines("ip_reserved"))
	if err != nil {
		return nil, fmt.Errorf("예약 IP: %w", err)
	}
	for _, r := range extra {
		if int(r.to-r.from) >= maxPoolSize {
			return nil, errors.New("예약 IP 범위가 너무 큽니다")
		}
		for n := r.from; ; n++ {
			p.reserved[n] = "예약"
			if n == r.to {
				break
			}
		}
	}
	return p, nil
}

// usable은 사람에게 배정해도 되는 주소인지 판단한다.
func (p *ipPool) usable(n uint32) bool {
	if _, ok := p.reserved[n]; ok {
		return false
	}
	// /31, /32가 아닌 서브넷이면 네트워크 주소와 브로드캐스트 주소는 쓸 수 없다
	if p.mask != 0 && p.mask < 0xFFFFFFFE {
		if n&^p.mask == 0 || n|p.mask == 0xFFFFFFFF {
			return false
		}
	}
	return true
}

// each는 배정 가능한 주소를 순서대로 돌며, f가 false를 돌려주면 멈춘다.
func (p *ipPool) each(f func(n uint32) bool) {
	for _, r := range p.ranges {
		for n := r.from; ; n++ {
			if p.usable(n) && !f(n) {
				return
			}
			if n == r.to {
				break
			}
		}
	}
}

func (p *ipPool) size() int {
	total := 0
	p.each(func(uint32) bool { total++; return true })
	return total
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// usedIPs는 사용 중인 주소(해제 완료 전 NAC 요청, 장비에 수동 기록된 IP)를 모은다.
func usedIPs(q queryer) (map[string]bool, error) {
	used := map[string]bool{}
	for _, query := range []string{
		`SELECT ip FROM nac_entries WHERE status != 'removed' AND ip != ''`,
		`SELECT ip FROM assets WHERE ip != '' AND status IN ('stock', 'assigned', 'repair')`,
	} {
		rows, err := q.Query(query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ip string
			rows.Scan(&ip)
			used[ip] = true
		}
		rows.Close()
	}
	return used, nil
}

// ipPoolStatus는 배정 가능한 주소 수와 그중 사용 중인 수를 돌려준다.
func (a *App) ipPoolStatus() (total, used int, err error) {
	p, err := a.loadIPPool()
	if err != nil {
		return 0, 0, err
	}
	inUse, err := usedIPs(a.db)
	if err != nil {
		return 0, 0, err
	}
	p.each(func(n uint32) bool {
		total++
		if inUse[u32ToIP(n)] {
			used++
		}
		return true
	})
	return total, used, nil
}

func pickFreeIP(p *ipPool, used map[string]bool) (string, error) {
	if len(p.ranges) == 0 {
		return "", errors.New("설정에 IP 대역이 없습니다. 설정 화면에서 IP 대역을 입력해 주세요")
	}
	found := ""
	p.each(func(n uint32) bool {
		if ip := u32ToIP(n); !used[ip] {
			found = ip
			return false
		}
		return true
	})
	if found == "" {
		return "", errors.New("배정 가능한 IP가 없습니다. 설정에서 IP 대역을 늘려 주세요")
	}
	return found, nil
}

// requestNAC는 장비 하나에 IP를 배정하고 NAC 등록 요청을 만든다. 이미 진행 중이면 그대로 둔다.
func (a *App) requestNAC(personID, assetID int64, actor string) error {
	a.ipMu.Lock()
	defer a.ipMu.Unlock()
	pool, err := a.loadIPPool()
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		ip, mac, err := a.requestNACTx(pool, personID, assetID)
		if err == nil {
			if ip != "" {
				a.audit(actor, "", "nac_request", fmt.Sprintf("asset:%d", assetID), mac+" "+ip)
			}
			return nil
		}
		if !isUniqueErr(err) {
			return err
		}
		lastErr = err // 다른 경로에서 같은 IP가 먼저 기록됨 → 다시 고른다
	}
	return fmt.Errorf("IP 배정 충돌이 반복되었습니다. 잠시 후 다시 시도해 주세요: %v", lastErr)
}

func (a *App) requestNACTx(pool *ipPool, personID, assetID int64) (ip, mac string, err error) {
	tx, err := a.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT mac FROM assets WHERE id = ?`, assetID).Scan(&mac); err != nil {
		return "", "", err
	}
	if mac == "" {
		return "", "", nil // MAC 없는 장비(모니터 등)는 네트워크 대상이 아니다
	}
	var n int
	tx.QueryRow(`SELECT COUNT(*) FROM nac_entries WHERE asset_id = ? AND status IN (?, ?)`, assetID, NacRequested, NacRegistered).Scan(&n)
	if n > 0 {
		return "", "", nil
	}
	used, err := usedIPs(tx)
	if err != nil {
		return "", "", err
	}
	if ip, err = pickFreeIP(pool, used); err != nil {
		return "", "", err
	}
	ts := nowTS()
	if _, err := tx.Exec(`INSERT INTO nac_entries(person_id, asset_id, mac, ip, status, requested_at) VALUES(?, ?, ?, ?, ?, ?)`,
		personID, assetID, mac, ip, NacRequested, ts); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`UPDATE assets SET ip = ?, updated_at = ? WHERE id = ?`, ip, ts, assetID); err != nil {
		return "", "", err
	}
	return ip, mac, tx.Commit()
}
