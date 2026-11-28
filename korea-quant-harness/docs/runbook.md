# 운영 런북 (초안)

## 즉시 중단
```bash
mkdir -p run && touch run/KILL      # 프로세스 상태와 무관하게 모든 신규 주문 차단
```
해제 순서: 원인 기록 → 포지션·미체결 대사 → `run/KILL` 삭제 → `KillSwitch.release(operator_id, approval_ref)`. **자동 재개는 없다.**

## 재시작
1. 프로세스 기동 (OMS는 대사 완료 전까지 신규 주문을 거부)
2. `startup_recovery()` 결과에서 `new_orders_allowed: true` 확인
3. false이면 `unresolved` 주문 ID와 `mismatches`를 확인하고 증권사 HTS/MTS에서 직접 대조

## 검증 명령
```bash
cd korea-quant-harness
python -m pytest -q                         # 전체 테스트
python scripts/generate_test_evidence.py    # docs/test_evidence.md 갱신
python scripts/run_paper.py                 # 오프라인 시연(예시 데이터) — 결과는 NO TRADE여야 정상
```

## 미정 (GATE 1 결정 필요)
운영자 부재 시 정책, 알림 채널, 2인 승인 여부
