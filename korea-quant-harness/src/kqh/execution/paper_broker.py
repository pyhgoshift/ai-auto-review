"""모의 브로커(장애 주입 지원).

체결 모델: 지정가 매수는 시장 체결가가 지정가 '미만'일 때만, 매도는 '초과'일 때만 체결(trade-through).
대기열 위치를 알 수 없으므로 지정가와 같은 가격의 체결은 체결로 보지 않는다(낙관 편향 방지).
"""
from __future__ import annotations

import uuid
from collections.abc import Callable
from dataclasses import dataclass, field, replace
from datetime import datetime
from decimal import Decimal

from ..errors import BrokerRejected, BrokerTimeout
from ..domain import Side, Venue, utcnow
from .broker_protocol import BrokerOrder


@dataclass
class PaperBroker:
    name: str = "paper"
    cash_krw: float = 0.0
    supported_venues: set[Venue] = field(default_factory=lambda: {Venue.KRX})
    supported_tickers: dict[Venue, set[str]] | None = None   # None = 제한 없음
    # 장애 주입
    timeout_after_accept_next: bool = False
    timeout_before_accept_next: bool = False
    _orders: dict[str, BrokerOrder] = field(default_factory=dict)
    _positions: dict[str, int] = field(default_factory=dict)
    submit_calls: int = 0
    clock: Callable[[], datetime] = utcnow

    async def submit_limit(self, ticker: str, venue: Venue, side: Side, qty: int, price: Decimal,
                           client_order_key: str) -> str:
        self.submit_calls += 1
        if self.timeout_before_accept_next:
            self.timeout_before_accept_next = False
            raise BrokerTimeout("simulated timeout before accept")
        if venue not in self.supported_venues:
            raise BrokerRejected(f"venue_not_supported:{venue}")
        if self.supported_tickers is not None and ticker not in self.supported_tickers.get(venue, set()):
            raise BrokerRejected(f"ticker_not_tradable_on_venue:{ticker}@{venue}")
        if side == Side.SELL and self._positions.get(ticker, 0) < qty:
            raise BrokerRejected("insufficient_position")
        oid = f"P{uuid.uuid4().hex[:10]}"
        self._orders[oid] = BrokerOrder(oid, ticker, venue, side, qty, price, 0, None, "OPEN", self.clock())
        if self.timeout_after_accept_next:
            self.timeout_after_accept_next = False
            raise BrokerTimeout("simulated timeout after accept (order exists at broker)")
        return oid

    async def cancel(self, broker_order_id: str) -> None:
        o = self._orders.get(broker_order_id)
        if o is None:
            raise BrokerRejected("order_not_found")
        if o.status == "OPEN":
            self._orders[broker_order_id] = replace(o, status="CANCELLED")

    async def list_orders(self) -> list[BrokerOrder]:
        return list(self._orders.values())

    async def positions(self) -> dict[str, int]:
        return {k: v for k, v in self._positions.items() if v}

    async def cash(self) -> float:
        return self.cash_krw

    # ---- 시뮬레이션 ----
    def fill(self, broker_order_id: str, qty: int, price: float) -> None:
        o = self._orders[broker_order_id]
        if o.status != "OPEN" or qty <= 0 or o.filled_qty + qty > o.qty:
            raise ValueError("invalid simulated fill")
        new = o.filled_qty + qty
        avg = price if o.avg_fill_price is None else (o.avg_fill_price * o.filled_qty + price * qty) / new
        self._orders[broker_order_id] = replace(o, filled_qty=new, avg_fill_price=avg,
                                                status="FILLED" if new == o.qty else "OPEN")
        sgn = 1 if o.side == Side.BUY else -1
        self._positions[o.ticker] = self._positions.get(o.ticker, 0) + sgn * qty
        self.cash_krw -= sgn * qty * price

    def on_market_trade(self, ticker: str, price: float, qty: int) -> None:
        for o in list(self._orders.values()):
            if o.ticker != ticker or o.status != "OPEN" or qty <= 0:
                continue
            crosses = price < float(o.limit_price) if o.side == Side.BUY else price > float(o.limit_price)
            if crosses:
                q = min(qty, o.qty - o.filled_qty)
                self.fill(o.broker_order_id, q, float(o.limit_price))
                qty -= q

    def set_position(self, ticker: str, qty: int) -> None:
        self._positions[ticker] = qty
