"""포트폴리오 장부(내부). 브로커 대사 결과로만 '진실'에 가까워진다."""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Position:
    ticker: str
    qty: int
    avg_price: float
    market_price: float
    sector: str
    daytrade: bool = True

    @property
    def value(self) -> float:
        return self.qty * self.market_price


@dataclass
class Portfolio:
    cash: float
    start_of_day_equity: float
    peak_equity: float
    realized_pnl_today: float = 0.0
    positions: dict[str, Position] = field(default_factory=dict)
    reserved_buy: dict[str, tuple[str, str, float]] = field(default_factory=dict)  # approval_id → (ticker, sector, 금액)
    reserved_sell_qty: dict[str, tuple[str, int]] = field(default_factory=dict)  # approval_id → (ticker, qty)

    @property
    def unrealized_pnl(self) -> float:
        return sum((p.market_price - p.avg_price) * p.qty for p in self.positions.values())

    @property
    def equity(self) -> float:
        return self.cash + sum(p.value for p in self.positions.values())

    @property
    def daily_pnl(self) -> float:
        return self.equity - self.start_of_day_equity

    @property
    def gross_exposure(self) -> float:
        return sum(p.value for p in self.positions.values()) + sum(v for _, _, v in self.reserved_buy.values())

    def sector_exposure(self, sector: str) -> float:
        held = sum(p.value for p in self.positions.values() if p.sector == sector)
        return held + sum(v for _, s, v in self.reserved_buy.values() if s == sector)

    def position_value(self, ticker: str) -> float:
        p = self.positions.get(ticker)
        held = p.value if p else 0.0
        return held + sum(v for t, _, v in self.reserved_buy.values() if t == ticker)

    def available_cash(self) -> float:
        return self.cash - sum(v for _, _, v in self.reserved_buy.values())

    def sellable_qty(self, ticker: str) -> int:
        held = self.positions[ticker].qty if ticker in self.positions else 0
        pending = sum(q for t, q in self.reserved_sell_qty.values() if t == ticker)
        return max(0, held - pending)
