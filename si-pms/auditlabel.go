package main

import (
	"regexp"
	"strings"
)

// 감사 로그는 작업 코드(영문)로 저장하고, 화면에는 한국어로 보여준다.
// 코드로 저장해 두어야 나중에 문구를 바꿔도 이전 기록과 함께 검색·집계할 수 있다.

var auditActionLabel = map[string]string{
	"login": "로그인", "profile_update": "이름 변경", "login_fail": "로그인 실패", "password_change": "비밀번호 변경",
	"person_create": "대리 등록", "person_update": "인력 정보 수정", "person_approve": "투입 승인",
	"person_reject": "반려", "person_offboard": "철수 시작", "person_complete": "철수 완료",
	"person_purge": "개인정보 파기", "person_delete": "등록 삭제", "badge_return": "출입증 반납",
	"link_issue": "개인 링크 발급", "self_join": "QR 셀프 등록", "self_submit": "본인 제출",
	"ack_offline": "서면·집합 이행 기록", "ack_bulk": "서면·집합 일괄 기록",
	"account_issue": "계정 발급", "account_revoke": "계정 회수",
	"asset_create": "자산 등록", "asset_update": "자산 수정", "asset_import": "자산 일괄 등록",
	"asset_assign": "자산 지급", "asset_return": "자산 반납", "asset_check": "보안 점검", "asset_status": "자산 상태 변경",
	"nac_request": "IP 배정·NAC 등록 요청", "nac_release": "NAC 해제 요청",
	"nac_register_done": "NAC 등록 완료", "nac_update_done": "NAC 만료일 변경 완료", "nac_remove_done": "NAC 해제 완료(IP 회수)",
	"doc_create": "문서 추가", "doc_update": "문서 수정",
	"export": "출력·다운로드", "template_upload": "양식 업로드", "template_reset": "기본 양식 복원",
	"settings_save": "설정 저장", "join_code_rotate": "등록코드 재발급",
	"user_create": "관리자 계정 추가", "user_reset": "비밀번호 초기화", "user_delete": "관리자 계정 삭제",
	"backup": "백업", "job_run": "자동 작업 수동 실행", "migrate": "DB 업그레이드",
	// 이전 버전 기록 호환
	"nac_registered": "NAC 등록 완료", "nac_removed": "NAC 해제 완료(IP 회수)", "daily_run": "일일 자동 작업",
}

var auditTargetLabel = map[string]string{"person": "인력", "asset": "자산", "doc": "문서", "ack": "서명"}

var targetRe = regexp.MustCompile(`^([a-z]+):(\d+)$`)

func auditLabel(code string) string {
	if l, ok := auditActionLabel[code]; ok {
		return l
	}
	return code
}

// targetLabel은 "person:12" → "인력 #12", 출력 대상 키는 대장 이름으로 바꾼다.
func targetLabel(t string) string {
	if m := targetRe.FindStringSubmatch(t); m != nil {
		if l, ok := auditTargetLabel[m[1]]; ok {
			return l + " #" + m[2]
		}
	}
	if l := ledgerByKey(t); l != nil {
		return l.Title
	}
	if strings.HasPrefix(t, "nac_") {
		if l, ok := nacKindLabel[strings.TrimPrefix(t, "nac_")]; ok {
			return "NAC " + l + " 파일"
		}
	}
	if j := jobByKey(t); j != nil {
		return j.Name
	}
	switch t {
	case "package":
		return "제출 패키지"
	case "outbox":
		return "보관된 제출 패키지"
	case "all":
		return "전체"
	}
	return t
}

// auditCodesMatching은 한국어 검색어에 해당하는 작업 코드들을 찾는다.
func auditCodesMatching(q string) []string {
	var out []string
	for code, label := range auditActionLabel {
		if strings.Contains(label, q) {
			out = append(out, code)
		}
	}
	return out
}
