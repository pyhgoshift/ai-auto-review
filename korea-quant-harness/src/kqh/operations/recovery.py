"""재시작 복구 순서. 모든 단계 성공 전까지 신규 주문 금지(OMS.recovered=False)."""
from __future__ import annotations

from ..execution.order_manager import OrderManager
from ..execution.reconciliation import reconcile_positions


async def startup_recovery(oms: OrderManager) -> dict:
    orders_ok = await oms.recover()
    recon = await reconcile_positions(oms.broker, oms.risk)
    ok = orders_ok and recon.matched
    if not ok:
        oms.recovered = False
    return {"orders_reconciled": orders_ok, "positions_matched": recon.matched, "mismatches": recon.mismatches,
            "new_orders_allowed": ok}
