"""주문 관리 엔진(OMS) — 브로커로 가는 유일한 경로.

SIGNAL → VALIDATION → HARD RISK CHECK(RiskApproval) → OMS.submit(approval) → BROKER

핵심 규칙
- 제출 전 주문 의도를 이벤트 저장소에 기록(기록 실패 시 제출하지 않음).
- 타임아웃 = 결과 불명(UNKNOWN). 재전송하지 않고 브로커 주문 목록으로 대사한다.
- 브로커 측 client key 지원이 검증되지 않았으므로 (종목·방향·수량·가격·시각) 매칭으로 대사하며,
  후보가 2개 이상이면 모호(AMBIGUOUS) → 자동 킬스위치.
- 재시작 후 recover()가 끝나기 전에는 어떤 신규 주문도 허용하지 않는다.
"""
from __future__ import annotations

import logging
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from decimal import Decimal
from enum import StrEnum

from ..data.event_store import EventStore
from ..errors import BrokerRejected, BrokerTimeout, InvalidTransition, OrderPathViolation
from ..domain import IntentKind, Side, Venue, utcnow
from ..risk.kill_switch import KillType
from ..risk.risk_engine import RiskApproval, RiskEngine
from .broker_protocol import Broker, BrokerOrder

log = logging.getLogger(__name__)


class OrderStatus(StrEnum):
    CREATED = "CREATED"
    RISK_APPROVED = "RISK_APPROVED"
    SUBMITTING = "SUBMITTING"
    ACKNOWLEDGED = "ACKNOWLEDGED"
    PARTIALLY_FILLED = "PARTIALLY_FILLED"
    FILLED = "FILLED"
    CANCEL_PENDING = "CANCEL_PENDING"
    CANCELLED = "CANCELLED"
    REJECTED = "REJECTED"
    UNKNOWN = "UNKNOWN"
    RECONCILING = "RECONCILING"


S = OrderStatus
TERMINAL = {S.FILLED, S.CANCELLED, S.REJECTED}
OPEN_LIKE = {S.SUBMITTING, S.ACKNOWLEDGED, S.PARTIALLY_FILLED, S.CANCEL_PENDING, S.UNKNOWN, S.RECONCILING}
ALLOWED: dict[OrderStatus, set[OrderStatus]] = {
    S.CREATED: {S.RISK_APPROVED, S.REJECTED},
    S.RISK_APPROVED: {S.SUBMITTING, S.REJECTED},
    S.SUBMITTING: {S.ACKNOWLEDGED, S.REJECTED, S.UNKNOWN},
    S.ACKNOWLEDGED: {S.PARTIALLY_FILLED, S.FILLED, S.CANCEL_PENDING, S.CANCELLED, S.REJECTED, S.RECONCILING},
    S.PARTIALLY_FILLED: {S.PARTIALLY_FILLED, S.FILLED, S.CANCEL_PENDING, S.CANCELLED, S.RECONCILING},
    S.CANCEL_PENDING: {S.CANCELLED, S.PARTIALLY_FILLED, S.FILLED, S.RECONCILING},
    S.UNKNOWN: {S.RECONCILING},
    S.RECONCILING: {S.ACKNOWLEDGED, S.PARTIALLY_FILLED, S.FILLED, S.CANCELLED, S.REJECTED, S.RECONCILING},
    S.FILLED: set(), S.CANCELLED: set(), S.REJECTED: set(),
}
MATCH_WINDOW = timedelta(seconds=30)
NOT_FOUND_CONFIRMATIONS = 2


@dataclass
class Order:
    order_intent_id: str
    approval_id: str
    kind: IntentKind
    strategy_id: str | None
    candidate_id: str | None
    signal_id: str | None
    client_order_key: str
    ticker: str
    venue: Venue
    side: Side
    qty: int
    limit_price: Decimal
    order_timestamp: datetime
    status: OrderStatus = S.CREATED
    broker_order_id: str | None = None
    filled_qty: int = 0
    avg_fill_price: float | None = None
    reject_reason: str | None = None
    not_found_count: int = 0
    history: list[tuple[str, str]] = field(default_factory=list)

    @property
    def remaining(self) -> int:
        return self.qty - self.filled_qty


class OrderManager:
    def __init__(self, broker: Broker, risk: RiskEngine, store: EventStore):
        self.broker, self.risk, self.store = broker, risk, store
        self.orders: dict[str, Order] = {}
        self._used_signals: set[str] = set()
        self.recovered = False

    # ---------- 내부 ----------
    def _transition(self, o: Order, to: OrderStatus, note: str = "") -> None:
        if to not in ALLOWED[o.status]:
            raise InvalidTransition(f"{o.status}->{to} ({o.order_intent_id})")
        o.history.append((o.status.value, to.value))
        o.status = to
        self.store.append("ORDER_STATE", {"order_intent_id": o.order_intent_id, "status": to.value, "note": note,
                                          "broker_order_id": o.broker_order_id, "filled_qty": o.filled_qty})

    def _refresh_unknown_flag(self) -> None:
        if any(o.status in (S.UNKNOWN, S.RECONCILING) for o in self.orders.values()):
            self.risk.set_flag("order_state_unknown", "orders awaiting reconciliation")
        else:
            self.risk.clear_flag("order_state_unknown")

    def has_unresolved(self, ticker: str | None = None) -> bool:
        return any(o.status in (S.UNKNOWN, S.RECONCILING) and (ticker is None or o.ticker == ticker)
                   for o in self.orders.values())

    def has_open(self, ticker: str, side: Side | None = None) -> bool:
        return any(o.ticker == ticker and o.status in OPEN_LIKE and (side is None or o.side == side)
                   for o in self.orders.values())

    # ---------- 제출 ----------
    async def submit(self, approval: RiskApproval, now: datetime | None = None) -> Order:
        now = now or utcnow()
        if not isinstance(approval, RiskApproval):
            raise OrderPathViolation("submit requires a RiskApproval issued by RiskEngine")
        if not self.recovered:
            raise OrderPathViolation("OMS not recovered/reconciled since start; new orders blocked")
        if self.has_unresolved(approval.ticker):
            raise OrderPathViolation("unresolved UNKNOWN order for ticker; reconcile first")
        if approval.kind == IntentKind.ENTRY:
            if approval.signal_id in self._used_signals:
                raise OrderPathViolation(f"duplicate order for signal {approval.signal_id}")
            if self.has_open(approval.ticker, Side.BUY):
                raise OrderPathViolation("conflicting open buy order")

        o = Order(str(uuid.uuid4()), approval.approval_id, approval.kind, approval.strategy_id,
                  approval.candidate_id, approval.signal_id, f"K{uuid.uuid4().hex[:16]}", approval.ticker,
                  approval.venue, approval.side, approval.qty, approval.limit_price, now)
        # 1) 제출 전 의도 기록 — 실패하면 제출하지 않는다
        try:
            self.store.append("ORDER_INTENT", {
                "order_intent_id": o.order_intent_id, "approval_id": o.approval_id, "kind": o.kind.value,
                "strategy_id": o.strategy_id, "candidate_id": o.candidate_id, "signal_id": o.signal_id,
                "client_order_key": o.client_order_key, "ticker": o.ticker, "venue": o.venue.value,
                "side": o.side.value, "qty": o.qty, "limit_price": str(o.limit_price),
                "order_timestamp": now.isoformat(),
                "risk_checks": [c.model_dump() for c in approval.checks]})
        except OSError as exc:
            self.risk.set_flag("storage_failure", str(exc))
            await self.risk.release(approval.approval_id)
            raise
        self.orders[o.order_intent_id] = o
        if o.signal_id:
            self._used_signals.add(o.signal_id)

        # 2) 제출 직전 위험 재검증(1회용 승인 소진)
        appr, fails = await self.risk.redeem(approval.approval_id, now)
        if appr is None:
            o.reject_reason = "pre_submit_recheck:" + ",".join(fails)
            self._transition(o, S.REJECTED, o.reject_reason)
            return o
        self._transition(o, S.RISK_APPROVED)
        self._transition(o, S.SUBMITTING)
        # 3) 전송 — 재시도 없음
        try:
            o.broker_order_id = await self.broker.submit_limit(o.ticker, o.venue, o.side, o.qty, o.limit_price,
                                                               o.client_order_key)
            self._transition(o, S.ACKNOWLEDGED)
        except BrokerTimeout as exc:
            self._transition(o, S.UNKNOWN, f"timeout:{exc}")
        except BrokerRejected as exc:
            o.reject_reason = exc.reason
            self._transition(o, S.REJECTED, exc.reason)
            await self.risk.release(o.approval_id)
        self._refresh_unknown_flag()
        return o

    async def cancel(self, order_intent_id: str) -> None:
        o = self.orders[order_intent_id]
        if o.status not in (S.ACKNOWLEDGED, S.PARTIALLY_FILLED):
            raise InvalidTransition(f"cannot cancel in {o.status}")
        self._transition(o, S.CANCEL_PENDING)
        try:
            await self.broker.cancel(o.broker_order_id)
        except BrokerTimeout:
            self._transition(o, S.RECONCILING, "cancel_timeout")
            self._refresh_unknown_flag()
            return
        await self.sync()   # 취소 확정은 브로커 상태 조회로만 판단

    # ---------- 대사 ----------
    def _match(self, o: Order, broker_orders: list[BrokerOrder]) -> list[BrokerOrder]:
        linked = {x.broker_order_id for x in self.orders.values() if x.broker_order_id}
        return [b for b in broker_orders
                if b.broker_order_id not in linked and b.ticker == o.ticker and b.side == o.side
                and b.qty == o.qty and b.limit_price == o.limit_price and b.venue == o.venue
                and abs(b.created_at - o.order_timestamp) <= MATCH_WINDOW]

    async def sync(self) -> None:
        """브로커 주문 목록을 진실원천으로 삼아 상태·체결을 갱신."""
        bos = await self.broker.list_orders()
        by_id = {b.broker_order_id: b for b in bos}
        for o in list(self.orders.values()):
            if o.status in TERMINAL:
                continue
            if o.status == S.UNKNOWN:
                self._transition(o, S.RECONCILING, "start_reconcile")
            if o.broker_order_id is None:
                cands = self._match(o, bos)
                if len(cands) == 1:
                    o.broker_order_id = cands[0].broker_order_id
                elif len(cands) > 1:
                    self.risk.kill.trigger(KillType.AUTOMATIC_KILL, f"ambiguous_reconcile:{o.order_intent_id}")
                    continue
                else:
                    o.not_found_count += 1
                    if o.not_found_count >= NOT_FOUND_CONFIRMATIONS:
                        o.reject_reason = "not_found_at_broker_after_reconcile"
                        self._transition(o, S.REJECTED, o.reject_reason)
                        await self.risk.release(o.approval_id)
                    continue
            b = by_id.get(o.broker_order_id)
            if b is None:
                self.risk.set_flag("broker_order_missing", o.order_intent_id)
                continue
            await self._apply_broker_state(o, b)
        self._refresh_unknown_flag()

    async def _apply_broker_state(self, o: Order, b: BrokerOrder) -> None:
        delta = b.filled_qty - o.filled_qty
        if delta < 0:
            self.risk.set_flag("fill_regression", o.order_intent_id)
            return
        if delta > 0:
            px = b.avg_fill_price if o.avg_fill_price is None else \
                (b.avg_fill_price * b.filled_qty - o.avg_fill_price * o.filled_qty) / delta
            o.filled_qty, o.avg_fill_price = b.filled_qty, b.avg_fill_price
            await self.risk.apply_fill(o.approval_id, o.side, o.ticker, delta, float(px))
            self.store.append("FILL", {"order_intent_id": o.order_intent_id, "qty": delta, "price": px,
                                       "cum_filled": o.filled_qty})
        target = {"FILLED": S.FILLED, "CANCELLED": S.CANCELLED, "REJECTED": S.REJECTED}.get(b.status)
        if target is None:
            target = S.PARTIALLY_FILLED if o.filled_qty > 0 else S.ACKNOWLEDGED
        if o.status == S.CANCEL_PENDING and target in (S.ACKNOWLEDGED, S.PARTIALLY_FILLED) and delta == 0:
            return   # 취소 확인 대기
        if target != o.status or (target == S.PARTIALLY_FILLED and delta > 0):
            if target in ALLOWED[o.status]:
                self._transition(o, target, "broker_sync")
            elif o.status == S.RECONCILING or target == o.status:
                pass
            else:
                self.risk.set_flag("state_mismatch", f"{o.order_intent_id}:{o.status}->{target}")
        if o.status in (S.CANCELLED, S.REJECTED, S.FILLED):
            await self.risk.release(o.approval_id)   # 잔여 예약 해제(부분체결 잔량은 장부에 유지)

    # ---------- 재시작 복구 ----------
    async def recover(self) -> bool:
        """이벤트 저장소에서 주문을 복원하고 브로커와 대사. 미해결이 남으면 False(신규 주문 차단 유지)."""
        self.recovered = False
        intents = {r["data"]["order_intent_id"]: r["data"] for r in self.store.read("ORDER_INTENT")}
        last_state: dict[str, dict] = {}
        for r in self.store.read("ORDER_STATE"):
            last_state[r["data"]["order_intent_id"]] = r["data"]
        for oid, d in intents.items():
            if oid in self.orders:
                continue
            st = last_state.get(oid, {})
            o = Order(oid, d["approval_id"], IntentKind(d["kind"]), d["strategy_id"], d["candidate_id"],
                      d["signal_id"], d["client_order_key"], d["ticker"], Venue(d["venue"]), Side(d["side"]),
                      int(d["qty"]), Decimal(d["limit_price"]), datetime.fromisoformat(d["order_timestamp"]),
                      broker_order_id=st.get("broker_order_id"), filled_qty=int(st.get("filled_qty") or 0))
            status = OrderStatus(st.get("status", "CREATED"))
            # 제출 중/생성 직후 중단된 주문은 결과 불명으로 간주
            o.status = S.UNKNOWN if status in (S.CREATED, S.RISK_APPROVED, S.SUBMITTING) else status
            self.orders[oid] = o
            if o.signal_id:
                self._used_signals.add(o.signal_id)
        self._refresh_unknown_flag()
        await self.sync()
        await self.sync()     # not-found 확인 2회
        self.recovered = not self.has_unresolved()
        self.store.append("RECOVERY", {"recovered": self.recovered,
                                       "unresolved": [o.order_intent_id for o in self.orders.values()
                                                      if o.status in (S.UNKNOWN, S.RECONCILING)]})
        return self.recovered
