"""버전 관리되는 호가 단위표와 가격 보정. 값은 설정 파일에서만 온다(하드코딩 금지)."""
from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import ROUND_CEILING, ROUND_FLOOR, Decimal

from ..errors import ConfigError
from ..domain import Market, Side


@dataclass(frozen=True)
class TickTable:
    version: str
    effective_from: date
    verified: bool
    markets: frozenset[Market]
    bands: tuple[tuple[Decimal | None, Decimal], ...]

    def tick_for(self, price: Decimal) -> Decimal:
        if price <= 0:
            raise ValueError("price must be positive")
        for upper, tick in self.bands:
            if upper is None or price < upper:
                return tick
        raise ConfigError("tick table has no open-ended band")


class TickRegistry:
    def __init__(self, raw_tables: list[dict]):
        tables = []
        for t in raw_tables:
            bands = tuple((None if u is None else Decimal(str(u)), Decimal(str(k))) for u, k in t["bands"])
            if bands[-1][0] is not None:
                raise ConfigError(f"tick table {t['version']}: last band must be open-ended")
            tables.append(TickTable(t["version"], date.fromisoformat(t["effective_from"]), bool(t["verified"]),
                                    frozenset(Market(m) for m in t["markets"]), bands))
        if not tables:
            raise ConfigError("no tick tables")
        self._tables = sorted(tables, key=lambda x: x.effective_from)

    def table_for(self, market: Market, on: date) -> TickTable:
        cands = [t for t in self._tables if market in t.markets and t.effective_from <= on]
        if not cands:
            raise ConfigError(f"no tick table for {market} on {on}")
        return cands[-1]


def round_to_tick(price: Decimal, table: TickTable, side: Side) -> Decimal:
    """매수는 내림, 매도는 올림(불리한 체결가 방지). 경계 구간은 보정 후 다시 확인한다."""
    tick = table.tick_for(price)
    mode = ROUND_FLOOR if side == Side.BUY else ROUND_CEILING
    p = (price / tick).to_integral_value(rounding=mode) * tick
    tick2 = table.tick_for(p)
    if tick2 != tick:
        p = (p / tick2).to_integral_value(rounding=mode) * tick2
    return p


def is_valid_tick(price: Decimal, table: TickTable) -> bool:
    return price > 0 and price % table.tick_for(price) == 0


def price_limit_band(base_price: Decimal, limit_pct: Decimal, table: TickTable) -> tuple[Decimal, Decimal]:
    """[검증 필요] 상·하한가 산출 방식(기준가·호가단위 절사)은 거래소 규정으로 재확인."""
    up = base_price * (1 + limit_pct / 100)
    dn = base_price * (1 - limit_pct / 100)
    return round_to_tick(dn, table, Side.SELL), round_to_tick(up, table, Side.BUY)
