"""호가창 상태. 가격 단위: KRW(Decimal), 수량 단위: 주(int).

구형(시퀀스가 더 작거나 같은) 스냅샷은 최신 상태를 덮어쓰지 못한다.
불완전하거나 교차(crossed)된 호가에서는 파생값(mid/OBI 등)을 만들지 않는다.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from decimal import Decimal

from ..domain import Venue, require_aware


@dataclass(frozen=True)
class Level:
    price: Decimal
    qty: int


@dataclass(frozen=True)
class BookUpdate:
    ticker: str
    venue: Venue
    sequence: int
    event_time: datetime        # 거래소 원시각
    received_time: datetime     # 수신 시각
    bids: tuple[Level, ...]     # 1→10단계, 가격 내림차순
    asks: tuple[Level, ...]     # 1→10단계, 가격 오름차순

    def __post_init__(self) -> None:
        require_aware(self.event_time, "event_time")
        require_aware(self.received_time, "received_time")


@dataclass
class OrderBook:
    ticker: str
    venue: Venue
    last: BookUpdate | None = None
    rejected_stale: int = 0
    issues: list[str] = field(default_factory=list)

    def apply(self, u: BookUpdate) -> bool:
        if u.ticker != self.ticker or u.venue != self.venue:
            raise ValueError("update for a different instrument/venue")
        if self.last is not None and u.sequence <= self.last.sequence:
            self.rejected_stale += 1
            return False
        self.last = u
        return True

    # ---- 유효성 ----
    def validity_issues(self) -> list[str]:
        if self.last is None:
            return ["no_book"]
        b, a = self.last.bids, self.last.asks
        out = []
        if not b or not a:
            out.append("empty_side")
            return out
        if any(l.qty < 0 or l.price <= 0 for l in b + a):
            out.append("invalid_level")
        if any(b[i].price <= b[i + 1].price for i in range(len(b) - 1)):
            out.append("bid_not_descending")
        if any(a[i].price >= a[i + 1].price for i in range(len(a) - 1)):
            out.append("ask_not_ascending")
        if b[0].price >= a[0].price:
            out.append("crossed_or_locked")
        return out

    def is_valid(self) -> bool:
        return not self.validity_issues()

    # ---- 파생값: 유효하지 않으면 None ----
    @property
    def best_bid(self) -> Decimal | None:
        return self.last.bids[0].price if self.is_valid() else None

    @property
    def best_ask(self) -> Decimal | None:
        return self.last.asks[0].price if self.is_valid() else None

    def mid(self) -> Decimal | None:
        if not self.is_valid():
            return None
        return (self.best_bid + self.best_ask) / 2

    def spread(self) -> Decimal | None:
        if not self.is_valid():
            return None
        return self.best_ask - self.best_bid

    def spread_bps(self) -> float | None:
        m, s = self.mid(), self.spread()
        if m is None or s is None or m <= 0:
            return None
        return float(s / m * 10000)

    def obi(self, depth: int = 10) -> float | None:
        """OBI = (ΣBid - ΣAsk) / (ΣBid + ΣAsk). 분모 0 또는 무효 호가 → None."""
        if not self.is_valid():
            return None
        sb = sum(l.qty for l in self.last.bids[:depth])
        sa = sum(l.qty for l in self.last.asks[:depth])
        if sb + sa == 0:
            return None
        return (sb - sa) / (sb + sa)

    def depth_qty(self, side_bids: bool, levels: int) -> int:
        if not self.is_valid():
            return 0
        lv = self.last.bids if side_bids else self.last.asks
        return sum(l.qty for l in lv[:levels])
