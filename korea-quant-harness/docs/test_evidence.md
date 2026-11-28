# 테스트 실행 증거 (자동 생성)

- 생성 시각(UTC): 2026-11-28T00:29:42+00:00
- Python: 3.13.18
- 실행 명령: `python -m pytest -q` (작업 디렉터리: `korea-quant-harness/`)
- 결과: PASS 56 / FAIL 0 / SKIP 0 (pytest 종료코드 0)
- '실제 결과'는 JUnit XML에서만 가져온다. 이 문서는 **소프트웨어 동작**의 증거이며 **전략 수익성의 증거가 아니다**.

| ID | 테스트 | 목적 | 입력 | 예상 결과 | 실제 결과 | 소스 |
|---|---|---|---|---|---|---|
| T01 | test_t01_prob_079_blocked | calibrated_prob 0.79 → 매수 금지 | calibrated_prob=0.79 | approved=False, blocked_by에 calibrated_prob | PASS | tests/unit/test_entry_gate.py |
| T02 | test_t02_prob_080_blocked_strict_inequality | 임계값 경계(0.80)는 엄격 초과 조건이므로 매수 금지 | calibrated_prob=0.80 | approved=False | PASS | tests/unit/test_entry_gate.py |
| T03 | test_t03_setup_quality_1_blocked | setup_quality=1 → 매수 금지 | setup_quality=1 | approved=False | PASS | tests/unit/test_entry_gate.py |
| T04 | test_t04_toxic_flow_blocked | toxic_flow=true → 매수 금지 | toxic_flow=True | approved=False | PASS | tests/unit/test_entry_gate.py |
| T05 | test_t05_near_limit_blocked_model_and_deterministic | risk_state=near_limit → 매수 금지(모델 값·결정론 값 각각) | 모델 risk_state=near_limit / 결정론 risk_state=near_limit | 둘 다 approved=False | PASS | tests/unit/test_entry_gate.py |
| T06 | test_t06_calibration_invalid_blocked | calibration_valid=false → 매수 금지 | calibration_valid=False | approved=False | PASS | tests/unit/test_entry_gate.py |
| T07 | test_t07_data_feed_stop_blocks_entry | 데이터 수신 중단 → 신규 진입 차단 | (a) 마지막 수신 후 3초 경과 (b) WebSocket 단절 후 재연결만 되고 gap 미복구 | stale_data / feed_disconnected / data_gap_unrecovered 사유, 스냅샷 None | PASS | tests/unit/test_state.py |
| T08 | test_t08_vi_blocks_entry_and_stabilization | VI 발동 → 신규 진입 차단, 해제 직후 안정화 기간 적용 | VI_ACTIVE → NORMAL 전환 후 60초/121초 | vi_vi_active → vi_stabilization → 통과 | PASS | tests/unit/test_state.py |
| T09 | test_t09_halted_security_blocks_entry | 거래정지 종목 → 신규 진입 차단 (상태 미확인도 차단) | halted=True, 그리고 새 종목(halted=None) | halted_or_unknown | PASS | tests/unit/test_state.py |
| T10 | test_t10_entry_blocked_from_1515 | 15:15 이후 데이트레이딩 신규 진입 차단 | 15:14:59 / 15:15:00 / 15:19:59 시각의 진입 요청 | 15:14:59만 허용, RiskEngine도 closing_gate 사유로 거부 | PASS | tests/unit/test_closing_calendar.py |
| T11 | test_t11_closing_auction_defense_and_unliquidated_kept | 15:20 동시호가 진입 방어, 미청산 잔고를 없어진 것으로 기록하지 않음 | 15:20:00, 데이트레이딩 잔고 {005930: 37}, 오버나이트 승인 {000660} | AUCTION_DEFENSE, 신규 진입 불가, 005930 37주 EMERGENCY 알림, 000660은 알림 제외 | PASS | tests/unit/test_closing_calendar.py |
| T12 | test_t12_partial_liquidation_keeps_remaining | 청산 주문 일부만 체결 → 잔여 수량 유지 | 보유 100주, 매도 100주 중 40주 체결 후 취소 | PARTIALLY_FILLED → CANCELLED, 내부/브로커 보유 60주, 매도가능 60주 | PASS | tests/fault_injection/test_oms_faults.py |
| T13 | test_t13_timeout_never_resubmits | 주문 API 타임아웃 → 중복 제출 금지, 대사로 해결 | 브로커가 주문을 접수한 뒤 응답 유실(timeout_after_accept) | UNKNOWN, 같은 종목 신규 주문 거부, submit_calls=1, sync 후 브로커 주문과 연결·플래그 해제 | PASS | tests/fault_injection/test_oms_faults.py |
| T13B | test_t13b_timeout_before_accept_resolved_as_not_found | 브로커가 실제로 접수하지 않은 타임아웃은 2회 미발견 확인 후 REJECTED | timeout_before_accept | UNKNOWN → RECONCILING → REJECTED, 예약 해제 | PASS | tests/fault_injection/test_oms_faults.py |
| T14 | test_t14_restart_reconciles_before_new_orders | 프로세스 재시작 → 미체결 주문 대사 전 신규 주문 금지 | 주문 A 접수(ACK) 후 프로세스 종료, 주문 B는 의도 기록 직후 중단(브로커 미접수) | 재시작 OMS는 recover 전 submit 거부; recover 후 A=ACKNOWLEDGED·B=REJECTED, 신규 허용 | PASS | tests/fault_injection/test_oms_faults.py |
| T15 | test_t15_daily_loss_limit_blocks_new_orders_but_not_exit | 일일 손실 한도 도달 → 신규 주문 금지, 청산 경로는 유지 | 보유 100주 평가손 -300,000원(한도) | risk_state=reduce, AUTOMATIC_KILL, 진입 거부, 청산 승인 | PASS | tests/unit/test_risk_engine.py |
| T16 | test_t16_kill_switch_blocks_and_requires_operator | 킬 스위치 작동 → 신규 주문 금지, 자동 해제 불가 | MANUAL_KILL / 외부 KILL 파일 / 승인 후 제출 직전 킬 | 진입 거부, 파일 존재 시 해제 실패, 운영자 정보 없는 해제 예외, 제출 직전 재검증으로 REJECTED | PASS | tests/unit/test_risk_engine.py |
| T17 | test_t17_spread_widening_recomputes_cost | 스프레드 급확대 → 거래비용 재계산, EV 음수 전환 시 진입 거부 | 스프레드 10bps → 60bps, 총 기대이익 W=150bps, L=60bps, p=0.6 | 비용 50bps 증가, EV 양수→음수, 게이트/위험엔진 모두 거부 | PASS | tests/unit/test_risk_engine.py |
| T18 | test_t18_negative_net_ev_blocked | 순기대값 음수·안전여유 미달·미산출 → 매수 금지 | net_ev_bps=-5, 5(여유 10bps 미만), None | 모두 approved=False | PASS | tests/unit/test_entry_gate.py |
| T19 | test_t19_disclosure_not_usable_before_publication | 공시 공개 시각 이전에는 공시 데이터 사용 금지 | 판단시각 10:00, 공시 공개 11:00(수신 11:00:03) | 10:00 사용 불가, 11:00:03 이후 사용 가능, available_at<published 생성 거부 | PASS | tests/unit/test_data_review_deploy.py |
| T20 | test_t20_unapproved_model_version_not_deployable | 미승인 모델 버전 → 실거래 배포 금지 | 승인 없음 / 해시 불일치 / PAPER 승인으로 LIVE 배포 / 섀도 테스트 미실행 / 위험한도 확대 미승인 | 모두 False, 모든 단계 PASS + 해시 일치 LIVE 승인만 True | PASS | tests/unit/test_data_review_deploy.py |
| T21 | test_t21_special_day_calendar | 특별 거래일 → 변경된 캘린더 적용, 미지의 날짜는 UNKNOWN | 2026-11-19(60분 지연 예시), 공휴일 2026-12-25, 범위 밖 2027-01-04 | 동시호가 16:20·방어 16:15 / NON_TRADING / UNKNOWN(진입 차단) | PASS | tests/unit/test_closing_calendar.py |
| T22 | test_t22_venue_order_condition_mismatch_rejected | 거래소별 주문 조건 불일치 → 주문 거부 | (a) venue_supports_order=False (b) 호가단위 위반 70,050원 (c) 가격제한폭 밖 | (a)(b)(c) 위험엔진 거부, (d) REJECTED 및 예약 해제 | PASS | tests/unit/test_risk_engine.py |
| T23 | test_t23_ledger_mismatch_blocks | 계좌 잔고와 내부 장부 불일치 → 신규 주문 차단 | 내부 100주, 브로커 90주 | ledger_mismatch 플래그, 진입 거부; 일치 후 해제 | PASS | tests/unit/test_risk_engine.py |
| T24 | test_t24_malformed_model_json_safe_neutral | 비정상 모델 JSON → 안전한 관망 | 깨진 JSON / 범위 밖 값 / 금지 필드(order_qty) / 문자열 bool | SafeNeutral 반환, 게이트 approved=False | PASS | tests/unit/test_entry_gate.py |
| T25 | test_t25_malicious_disclosure_not_executed | 공시 문서의 악성 지시문 → 명령 실행 거부 | 매수 지시·킬스위치 해제·경계태그 위조·셸 명령이 섞인 공시 본문 | injection_suspected=True, 경계 태그 이스케이프, 킬스위치 상태 불변, 주문 필드 포함 출력은 스키마 거부 | PASS | tests/unit/test_injection_and_isolation.py |
| - | test_ai_layers_cannot_import_order_path[reflex] | AI(SYSTEM 1/2)·리뷰 계층이 주문 전송 계층에 직접 접근 불가(정적 검증) | research/, reflex/, review/ 의 import 구문 | execution·broker·risk_engine 모듈 import 없음 | PASS | tests/unit/test_injection_and_isolation.py |
| - | test_ai_layers_cannot_import_order_path[research] | AI(SYSTEM 1/2)·리뷰 계층이 주문 전송 계층에 직접 접근 불가(정적 검증) | research/, reflex/, review/ 의 import 구문 | execution·broker·risk_engine 모듈 import 없음 | PASS | tests/unit/test_injection_and_isolation.py |
| - | test_ai_layers_cannot_import_order_path[review] | AI(SYSTEM 1/2)·리뷰 계층이 주문 전송 계층에 직접 접근 불가(정적 검증) | research/, reflex/, review/ 의 import 구문 | execution·broker·risk_engine 모듈 import 없음 | PASS | tests/unit/test_injection_and_isolation.py |
| - | test_brier_ece_and_calibration_validity | Brier/ECE 계산과 보정 유효성 판정(표본 부족·in-sample 거부) | 완전 예측, 과신 예측, 표본 100개 | Brier 0, 과신 ECE 큼, 표본 부족·in-sample은 valid=False | PASS | tests/unit/test_data_review_deploy.py |
| - | test_concurrent_approvals_cannot_exceed_exposure | 동시 주문에서도 위험 예산이 원자적으로 예약되어 한도 초과 불가 | 총익스포저 한도 2,000만원, 300만원 주문 20건 동시 승인 요청 | 승인 합계 ≤ 2,000만원 (42주×6건 + 잔여 33주 1건 = 7건) | PASS | tests/unit/test_risk_engine.py |
| - | test_control_all_conditions_pass | 게이트가 무조건 차단만 하는 것이 아님을 확인(양성 대조군) | 모든 조건 충족 신호/컨텍스트 | approved=True | PASS | tests/unit/test_entry_gate.py |
| - | test_control_entry_approved_with_caps | 정상 조건에서 승인되며 수량은 가장 작은 한도로 제한됨(양성 대조군) | desired 20주 @70,000, 주문금액 상한 300만원 | approved, qty=min(20, 42)=20; desired 100 → order_value 상한 42주 | PASS | tests/unit/test_risk_engine.py |
| - | test_duplicate_signal_and_forged_approval_rejected | 동일 신호 중복 주문 방지, 위험엔진이 발급하지 않은 승인서 거부 | 같은 signal_id 두 번 / 위조 approval_id / dict 객체 | OrderPathViolation 또는 REJECTED(unknown_approval) | PASS | tests/fault_injection/test_oms_faults.py |
| - | test_escalation_low_prob_and_crisis | prob<0.60 또는 crisis → 진입 차단 + SYSTEM 2 상향 | calibrated_prob=0.55 / regime=crisis | escalate_to_system2=True, approved=False | PASS | tests/unit/test_entry_gate.py |
| - | test_event_store_hash_chain_detects_tamper | 감사 로그 불변성 — 변조 탐지 | 3건 기록 후 1건 수정 | 변조 전 True, 후 False | PASS | tests/unit/test_data_review_deploy.py |
| - | test_expired_signal_blocked | 허용 지연 초과 신호 폐기 | now가 signal_valid_until 이후 | signal_not_expired 실패 | PASS | tests/unit/test_entry_gate.py |
| - | test_kelly_zero_conditions_and_cap | Kelly 0 처리 조건과 Quarter Kelly 상한 | 보정 실패/표본 부족/손실꼬리/EV≤0, 정상 통계(p=0.55,b=1.5,n=1000) | 비정상은 0, 정상은 0 < f ≤ 0.25·f_full(하한 승률 사용) | PASS | tests/unit/test_risk_engine.py |
| - | test_live_requires_live_approval_and_verified_reference_data | LIVE 모드는 LIVE_APPROVED 한도 + 검증된 기준데이터(캘린더·호가단위·세율)가 모두 필요 | PAPER_APPROVED 한도로 LIVE / LIVE_APPROVED지만 reference_data_verified=False | 둘 다 거부 | PASS | tests/unit/test_risk_engine.py |
| - | test_missing_limits_blocks_everything | 위험 한도 설정 누락 → 신규 주문 금지 | limits=None | approved=False | PASS | tests/unit/test_entry_gate.py |
| - | test_missing_or_partial_limits_file_returns_none | 설정 누락/필드 누락 시 None(거래 금지) | 없는 파일, 필드 하나가 빠진 파일 | None | PASS | tests/unit/test_risk_engine.py |
| - | test_near_limit_state | 한도 사용률 70% 이상 → near_limit(신규 진입 불가) | 일일손실 -220,000원(73%) | near_limit | PASS | tests/unit/test_risk_engine.py |
| - | test_nightly_includes_untraded_signals | 야간 평가가 매매하지 않은 신호도 포함(선택 편향 통제), 자동 배포 없음 | traded 2건 + untraded 2건 | n_untraded=2, auto_deploy=False | PASS | tests/unit/test_data_review_deploy.py |
| - | test_obi_bounded_or_none | OBI ∈ [-1,1] 또는 None(분모 0) | 임의 잔량 | 범위 유지 | PASS | tests/property/test_properties.py |
| - | test_obi_not_fabricated_on_invalid_book | 분모 0 또는 교차 호가에서 OBI/mid 생성 금지 | 잔량 0 호가, 교차 호가 | None | PASS | tests/unit/test_state.py |
| - | test_only_oms_calls_broker_submit | 주문 경로 단일성 — submit_limit 호출은 order_manager.py에만 존재 | src 전체 AST | 호출 위치 = {execution/order_manager.py} | PASS | tests/unit/test_injection_and_isolation.py |
| - | test_ready_state_builds_compact_snapshot | 정상 상태에서만 스냅샷 생성, 모델 입력 400토큰 미만 | 정상 호가·연결·VI NORMAL·계좌동기화 | readiness=[], 스냅샷 생성 및 토큰 상한 이내 | PASS | tests/unit/test_state.py |
| - | test_reduce_cannot_exceed_position | 청산 경로는 보유 수량을 넘을 수 없음(공매도·포지션 반전 방지) | 보유 10주, 매도 11주 | reduce_only 실패 | PASS | tests/fault_injection/test_oms_faults.py |
| - | test_round_to_tick_valid_and_conservative | 호가 보정 결과는 항상 유효 호가이며 매수는 ≤ 원가, 매도는 ≥ 원가 | 1~3,000,000원 임의 가격 | 불변조건 유지 | PASS | tests/property/test_properties.py |
| - | test_shipped_unapproved_limits_block | 저장소에 포함된 위험 한도(UNAPPROVED)로는 PAPER 주문도 불가 | configs/risk_limits.yaml 원본 | approval_status_for_mode 실패 | PASS | tests/unit/test_risk_engine.py |
| - | test_stale_sequence_cannot_overwrite_latest | 구형 스냅샷이 최신 상태를 덮어쓰지 못함 | seq 5 적용 후 seq 4 도착 | seq 4 거부, 최신 유지 | PASS | tests/unit/test_state.py |
| - | test_storage_failure_prevents_submit | 주문 의도 기록 실패 시 제출하지 않음 | EventStore.append가 OSError | 예외 전파, submit_calls=0, storage_failure 플래그 | PASS | tests/fault_injection/test_oms_faults.py |
| - | test_stream_validator_dup_order_ticker | 중복·순서역전·잘못된 종목코드 폐기 | 동일 해시 재수신, seq 역전, 'ABC' 코드 | 각각 False | PASS | tests/unit/test_data_review_deploy.py |
| - | test_unliquidatable_recorded_as_emergency | 청산 불가능 상태(거래정지 등)는 비상 위험으로 기록 | tradable=False | 거부 + unliquidatable 플래그 | PASS | tests/fault_injection/test_oms_faults.py |
| - | test_venues_modeled_separately | KRX와 NXT를 별도 거래장소로 모델링 | 08:10 (NXT 프리마켓 시간대, KRX 정규장 전) | NXT=PRE_MARKET, KRX=CLOSED | PASS | tests/unit/test_closing_calendar.py |
| - | test_websocket_bounded_reconnect_and_gap_flag | 제한된 재연결, gap 미복구 시 차단 상태 유지 | 연결 계속 실패(시도 3회 제한) / 연결 성공 + gap 복구 실패 | FAILED, gap 미복구 시 on_recovered 미호출 | PASS | tests/unit/test_data_review_deploy.py |
