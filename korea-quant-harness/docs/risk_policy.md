# 위험 정책 (PHASE 1 산출물)

## 1. 원칙
- 위험 한도는 `configs/risk_limits.yaml`에서만 읽는다. **필드가 하나라도 없거나 형식이 틀리면 로딩 결과는 None이고, 모든 신규 진입이 금지된다.**
- 저장소에 들어 있는 값은 **[예시 데이터 - 운영자 승인 필요]**이며 `approval.status: UNAPPROVED`로 출하한다. 이 상태에서는 PAPER 주문도 불가능하다.
- AI는 한도를 만들거나 바꿀 수 없다(스키마에 해당 필드가 없음). Quarter Kelly 상한(0.25)과 재전송 0회는 **스키마로 강제**하므로 설정 파일로도 완화할 수 없다.

## 2. 결정론적 위험 상태
`utilization = max(일일손실/한도, 일일낙폭/한도, 누적낙폭/한도)`
- `< near_limit_utilization_pct` → **safe**
- `≥ near_limit_utilization_pct` → **near_limit** (신규 진입 불가)
- `≥ 100%` → **reduce** + 자동 킬 스위치(AUTOMATIC_KILL)

## 3. 수량 = 아래 한도 중 최소값 (`RiskEngine._qty_caps`)
Kelly(사이징) 희망 수량 · 종목 비중 · 업종 비중 · 총 익스포저(대기 주문 포함) · 1회 주문 금액 · ADV 참여율 · 호가 잔량 참여율 · 가용 현금(예약분 차감) · **잔여 일일손실 예산 ÷ 손절폭**

## 4. 킬 스위치
| 종류 | 작동 | 해제 |
|---|---|---|
| MANUAL_KILL | `kill.trigger()` 또는 **외부 파일 `run/KILL` 생성**(프로세스 밖 통제) | 운영자 ID와 승인 참조가 있어야 함. 파일이 남아 있으면 해제 불가 |
| AUTOMATIC_KILL | 손실 한도, 대사 모호성 | 자동 해제 없음 |
| BROKER_SIDE_PROTECTION | 미검증 → 호출 시 `NotImplementedError` | — |
작동하면: 신규 매수 즉시 금지 → (OMS) 미체결 주문 점검 → 포지션 재조회 → 청산 가능 여부 평가 → 운영자 통보.

## 5. 시스템 플래그 (하나라도 있으면 신규 진입 금지)
`order_state_unknown`, `ledger_mismatch`, `account_query_failed`, `storage_failure`, `fill_regression`, `state_mismatch`, `broker_order_missing`, `unliquidatable:<ticker>`

## 6. 마감 동시호가 (일반일 기준, 특별일은 동시호가 시작 시각에서 역산)
| 구간 | 동작 |
|---|---|
| 15:15:00~15:19:59 | 신규 진입 전면 차단(설정으로 데이트레이딩만 차단하도록 완화 가능), 데이트레이딩 청산 시도 |
| 15:20:00~ | 동시호가 방어. 미청산 잔고는 **EMERGENCY 알림 + 장부에 그대로 유지** |
| 15:30~ | 대사, 오버나이트 위험 보고 |

## 7. 운영자가 결정해야 할 정책 (GATE 1)
1. 운영자와 연락이 안 될 때 킬 스위치가 켜진 상태라면, 보유 포지션을 (a) 유지할지 (b) 사전 승인된 규칙으로 청산할지
2. 정정·취소 비율 상한(시장교란 오인 방지)
3. 실제 한도 수치와 그 근거(손실 감내 금액, 계좌 규모)
4. 15:15 전면 차단 유지 여부
