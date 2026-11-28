# korea-quant-harness

KOSPI/KOSDAQ 자율 리서치·감시·검증·모의주문 하네스. **생존 우선, PAPER 기본, LIVE는 운영자 승인 필요.**

> 현재 판정: **A. RESEARCH ONLY** — GATE 1(명세 승인) 대기 중. 외부 API 연동, 실데이터, 실계좌 주문은 없다.
> 이 코드가 정상 동작한다는 사실은 **어떤 전략의 수익성도 입증하지 않는다.**

## 빠른 확인
```bash
pip install "pydantic>=2.6" pyyaml pytest hypothesis
cd korea-quant-harness
python -m pytest -q                        # 56 passed (T01–T25 포함)
python scripts/generate_test_evidence.py   # docs/test_evidence.md 재생성
python scripts/run_paper.py                # 오프라인 시연 → "NO TRADE"
```

## 문서 (읽는 순서)
1. `docs/spec_review.md` — 원본 명세의 결함 D1~D14, 세 가지 관점 비교, 자기 점검
2. `docs/approval_gates.md` — GATE 1 결정 항목, 12개 불변조건 감사, 적대적 검토, 현재 판정
3. `docs/specification.md`, `docs/risk_policy.md`, `docs/api_inventory.md`
4. `docs/architecture.md`, `docs/runbook.md`, `docs/test_evidence.md`

## 구현 상태
| 영역 | 상태 |
|---|---|
| Reflex 스키마, 진입 게이트, 보정 상태 | 구현·테스트 |
| 상태 엔진(호가, OBI, 스냅샷 <400토큰, VI, 단절/gap) | 구현·테스트 |
| 캘린더, 호가단위, 동시호가 게이트 | 구현·테스트 (기준 데이터는 `verified: false`) |
| 위험 엔진(원자적 예약, 1회용 승인서, ENTRY/REDUCE 분리), 킬 스위치, 사이징 | 구현·테스트 |
| OMS(상태 머신, 재전송 금지, 대사, 재시작 복구), 모의 브로커(장애 주입) | 구현·테스트 |
| 이벤트 저장소(해시 체인), 데이터 계약(point-in-time), Brier/ECE, 배포 게이트, 인젝션 방어 | 구현·테스트 |
| KIS / DART / KRX / ECOS / Claude 연동 | **미구현 (NotVerifiedError)** — `docs/api_inventory.md` |
| 백테스트, 스케줄러, 알림 채널, LIVE 경로 | **미구현** |

## 원본 명세에서 바꾼 점 (요약)
- 확률 0.80보다 **비용 차감 순기대값의 보수적 하한**을 주된 진입 기준으로 함
- 단일 OMS + **ENTRY/REDUCE 승인서 분리**(킬 스위치는 청산을 막지 않음)
- 제출 직전 재검증을 1회용 승인서로 **구조적으로 강제**
- 타임아웃 재전송을 스키마로 금지하고 매칭 대사로 해결
- 15:15 이후 **신규 진입 전면 차단**(설정 가능), 미확인 상태(None)는 차단
- 위험 한도는 `UNAPPROVED`로 출하
