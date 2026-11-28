"""브로커 어댑터 프로토콜. OMS만 이 인터페이스를 호출한다."""
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from decimal import Decimal
from typing import Protocol

from ..domain import Side, Venue


@dataclass(frozen=True)
class BrokerOrder:
    broker_order_id: str
    ticker: str
    venue: Venue
    side: Side
    qty: int
    limit_price: Decimal
    filled_qty: int
    avg_fill_price: float | None
    status: str           # OPEN | FILLED | CANCELLED | REJECTED
    created_at: datetime


class Broker(Protocol):
    name: str

    async def submit_limit(self, ticker: str, venue: Venue, side: Side, qty: int, price: Decimal,
                           client_order_key: str) -> str:
        """broker_order_id 반환. BrokerTimeout은 '실패'가 아니라 '결과 불명'이다."""
        ...

    async def cancel(self, broker_order_id: str) -> None: ...

    async def list_orders(self) -> list[BrokerOrder]: ...

    async def positions(self) -> dict[str, int]: ...

    async def cash(self) -> float: ...
