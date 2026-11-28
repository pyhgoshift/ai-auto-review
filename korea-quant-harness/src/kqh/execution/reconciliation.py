"""계좌 대사: 내부 장부 vs 브로커 잔고. 불일치 시 신규 주문 차단 플래그."""
from __future__ import annotations

from dataclasses import dataclass

from ..risk.risk_engine import RiskEngine
from .broker_protocol import Broker


@dataclass(frozen=True)
class ReconReport:
    matched: bool
    mismatches: dict[str, tuple[int, int]]   # ticker → (internal, broker)


async def reconcile_positions(broker: Broker, risk: RiskEngine) -> ReconReport:
    try:
        bpos = await broker.positions()
    except Exception as exc:  # 조회 실패 자체가 위험
        risk.set_flag("account_query_failed", type(exc).__name__)
        return ReconReport(False, {})
    internal = {t: p.qty for t, p in risk.portfolio.positions.items() if p.qty}
    keys = set(bpos) | set(internal)
    mm = {k: (internal.get(k, 0), bpos.get(k, 0)) for k in keys if internal.get(k, 0) != bpos.get(k, 0)}
    if mm:
        risk.set_flag("ledger_mismatch", str(sorted(mm)))
    else:
        risk.clear_flag("ledger_mismatch")
        risk.clear_flag("account_query_failed")
    return ReconReport(not mm, mm)
