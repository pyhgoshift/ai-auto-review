# 아키텍처 (초안 — GATE 2 대상)

## 1. 단일 주문 경로

```mermaid
flowchart LR
  subgraph AI["AI 계층 (주문 계층 import 금지: 정적 테스트)"]
    B[SYSTEM 2 BRAIN<br/>research/] --> C[Candidate]
    R[SYSTEM 1 REFLEX<br/>reflex/inference] --> S[ReflexSignal]
  end
  D[DATA → validators → StateEngine] --> SNAP[StateSnapshot <400tok]
  SNAP --> R
  S --> V[signal_validator<br/>EntryDecision]
  V --> RE[RiskEngine.approve_entry<br/>원자적 예약 → RiskApproval]
  RE --> OMS[OrderManager.submit]
  OMS -->|redeem: 제출 직전 재검증| RE
  OMS -->|유일한 submit_limit 호출부| BR[Broker: Paper / KIS 미구현]
  BR --> OMS
  OMS --> ES[(EventStore<br/>해시 체인)]
  K[KillSwitch + 외부 KILL 파일] -.차단.-> RE
```

- `submit_limit`를 호출하는 곳은 `execution/order_manager.py` 하나뿐이다(AST 테스트로 확인).
- 신호(`ReflexSignal`), 진입 판정(`EntryDecision`), 주문 승인(`RiskApproval`), 주문(`Order`)은 각각 다른 자료구조다.

## 2. 주문 상태 머신

```mermaid
stateDiagram-v2
  [*] --> CREATED
  CREATED --> RISK_APPROVED: redeem OK
  CREATED --> REJECTED: redeem 실패
  RISK_APPROVED --> SUBMITTING
  SUBMITTING --> ACKNOWLEDGED
  SUBMITTING --> REJECTED: 브로커 거부
  SUBMITTING --> UNKNOWN: 타임아웃(재전송 금지)
  UNKNOWN --> RECONCILING
  RECONCILING --> ACKNOWLEDGED: 매칭 1건
  RECONCILING --> REJECTED: 2회 미발견
  RECONCILING --> RECONCILING: 매칭 2건 이상 → AUTOMATIC_KILL
  ACKNOWLEDGED --> PARTIALLY_FILLED
  PARTIALLY_FILLED --> FILLED
  ACKNOWLEDGED --> CANCEL_PENDING
  PARTIALLY_FILLED --> CANCEL_PENDING
  CANCEL_PENDING --> CANCELLED
  FILLED --> [*]
  CANCELLED --> [*]
  REJECTED --> [*]
```

## 3. 장애 복구
재시작 → `OMS.recovered=False`(신규 주문 거부) → 이벤트 저장소에서 주문 복원(제출 중이던 주문은 UNKNOWN 처리) → 브로커 주문 목록으로 2회 대사 → 포지션 대사 → 둘 다 성공해야 신규 주문을 허용한다 (`operations/recovery.py`, T14).

## 4. GATE 2에서 확정할 설계 (아직 미완)
- **프로세스 분리**: 주문 프로세스(실계좌 키를 가진 유일한 프로세스)와 리서치·모델 프로세스를 나누고, 그 사이에는 승인서만 오가는 IPC를 둔다. 현재는 단일 프로세스 내 논리 분리에 그친다.
- 영속 저장소: JSONL에서 SQLite/Postgres로 전환(트랜잭션과 동시 접근 필요 시)
- 현금 대사(수수료·세금 반영), 시각원(NTP) 연동, 알림 채널
