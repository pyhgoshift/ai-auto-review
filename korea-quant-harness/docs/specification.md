# 시스템 명세 (PHASE 1 산출물, v0.1 — 운영자 승인 대기)

원본 운영 명세 v2.0을 기준으로 하되, `spec_review.md`의 수정 사항(D1~D14)을 반영한다. 충돌하는 부분은 이 문서가 우선한다.

## 1. 목표와 비목표
- **목표**: 파산 확률 최소화 → 검증된 양(+)의 순기대값 → 장기 위험조정수익. 모든 데이터와 주문을 추적할 수 있어야 한다.
- **비목표**: 단기 수익률 극대화, 24시간 매매(한국 주식은 불가), 운영자 승인 없는 실거래 및 배포.
- **시스템 원칙**: `NO VERIFIED EDGE / NO VALID DATA / NO RISK APPROVAL → NO TRADE; NO OPERATOR AUTHORIZATION → NO LIVE`.

## 2. 권한 모델
| 주체 | 할 수 있는 것 | 할 수 없는 것 |
|---|---|---|
| SYSTEM 2 (BRAIN) | 후보·논제·체제 판단·개선안 작성 (스키마로 검증된 JSON) | 주문, 수량, 한도, 킬 스위치, 배포 |
| SYSTEM 1 (REFLEX) | `ReflexSignal` 생성 (추가 필드 거부) | 위와 동일 + 수량 결정 |
| Signal Validator | 진입 조건 판정 (`EntryDecision`) | 주문 승인 |
| Risk Engine | 승인서 발급·재검증·예산 예약 (`RiskApproval`) | 위험 한도 생성·변경 |
| OMS | 승인서를 가진 주문만 브로커에 전송 | 승인서 없이 전송, 재전송 |
| 운영자 | 한도 승인, 킬 스위치 해제, 게이트 승인, 배포 | — |

## 3. 신규 진입 조건 (모두 충족해야 함, 각 조건의 통과·차단 근거를 기계 판독 가능하게 기록)
명세 §7의 14개 조건에 다음을 더한다: 신호 만료, 스냅샷 만료, crisis 체제, **순기대값 > 안전여유(bps)**.
이어서 위험 엔진이 다음을 확인한다: 한도 로딩, 모드별 승인 상태, 킬 스위치, 시스템 플래그, 결정론적 위험 상태, 동시호가 게이트, 거래장소 지원 여부, 호가단위, 가격제한폭, 스프레드, API 지연, 손절 정의, 일평균 거래대금(ADV) 확인, 수량 상한, (LIVE 전용) 기준 데이터 검증 여부.

## 4. 청산 (REDUCE) 경로
- 보유 수량 − 대기 중 매도 수량 이하만 허용한다(공매도와 포지션 반전 불가).
- 킬 스위치와 손실 한도는 청산을 막지 않는다. 청산할 수 없는 경우(거래정지 등)는 `unliquidatable:<ticker>` 비상 플래그로 남긴다.

## 5. 주문 상태 머신
`CREATED → RISK_APPROVED → SUBMITTING → {ACKNOWLEDGED | REJECTED | UNKNOWN}`; `UNKNOWN → RECONCILING → {ACK…|FILLED|CANCELLED|REJECTED}`. 허용되지 않은 전이는 예외로 막는다(`order_manager.ALLOWED`). 주문 의도는 **제출 전에** 기록하며, 기록에 실패하면 제출하지 않는다.

## 6. 데이터 계약
모든 이벤트는 `DataEnvelope`(source_id, instrument_id, venue, event_time, published_time, received_time, available_at, source_sequence, schema_version, data_quality_status, payload_hash)를 따른다. `available_at ≥ max(published, received)`를 스키마가 강제하며, 판단 시점보다 나중에 사용 가능해진 데이터는 사용하지 않는다.

## 7. 시간
모든 시각은 timezone-aware여야 한다. 내부 판단은 Asia/Seoul을 기준으로 하고, 원본 거래소 시각과 수신 시각은 따로 보존한다.

## 8. 승인 기준(제안 — 운영자 확정 필요)
| 게이트 | 통과 기준(제안) |
|---|---|
| GATE 1 | 이 문서, api_inventory, risk_policy 승인. §8 결정 항목(approval_gates.md)에 답변 |
| GATE 3 | 데이터 수집기와 KIS **모의투자** 어댑터가 통합 테스트 통과. 실계좌 비활성 |
| GATE 5 | 사전 등록한 out-of-sample 기간에서 비용 차감 순기대값 하한 > 0, ECE ≤ 한도, **벤치마크 대비 초과**, 시도 횟수를 반영한 보정 |
| GATE 6 | 소액 한도, 비상 중단 리허설, 계좌 대사 무결점 N영업일 |
