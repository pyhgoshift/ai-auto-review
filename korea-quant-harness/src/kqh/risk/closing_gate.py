"""마감 동시호가 방어. 기준 시각은 캘린더의 '동시호가 시작'에서 역산(일반일 15:20 → 15:15)."""
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from enum import StrEnum

from ..state.market_clock import TradingCalendar
from ..domain import to_kst


class ClosingPhase(StrEnum):
    UNKNOWN = "UNKNOWN"
    NON_TRADING = "NON_TRADING"
    NORMAL = "NORMAL"
    ENTRY_BLOCK_LIQUIDATE = "ENTRY_BLOCK_LIQUIDATE"   # 15:15:00~15:19:59
    AUCTION_DEFENSE = "AUCTION_DEFENSE"               # 15:20:00~15:29:59
    POST_CLOSE = "POST_CLOSE"                         # 15:30 이후


@dataclass(frozen=True)
class UnliquidatedAlert:
    ticker: str
    remaining_qty: int
    phase: ClosingPhase
    severity: str
    note: str


class ClosingGate:
    def __init__(self, cal: TradingCalendar):
        self.cal = cal

    def phase_at(self, now: datetime) -> ClosingPhase:
        k = to_kst(now)
        td = self.cal.is_trading_day(k.date())
        if td is None:
            return ClosingPhase.UNKNOWN
        if not td:
            return ClosingPhase.NON_TRADING
        auc = self.cal.closing_auction_start(k.date())
        close = self.cal.regular_close(k.date())
        if auc is None or close is None:
            return ClosingPhase.UNKNOWN
        if k >= close:
            return ClosingPhase.POST_CLOSE
        if k >= auc:
            return ClosingPhase.AUCTION_DEFENSE
        if k >= auc - self.cal.entry_block_offset:
            return ClosingPhase.ENTRY_BLOCK_LIQUIDATE
        return ClosingPhase.NORMAL

    def new_entry_allowed(self, now: datetime, is_daytrade: bool = True) -> bool:
        ph = self.phase_at(now)
        if ph == ClosingPhase.NORMAL:
            return True
        if ph == ClosingPhase.ENTRY_BLOCK_LIQUIDATE and not is_daytrade and not self.cal.block_all_new_entries:
            return True
        return False

    def should_liquidate_daytrade(self, now: datetime) -> bool:
        return self.phase_at(now) in (ClosingPhase.ENTRY_BLOCK_LIQUIDATE, ClosingPhase.AUCTION_DEFENSE)

    def unliquidated(self, now: datetime, daytrade_positions: dict[str, int],
                     overnight_approved: set[str]) -> list[UnliquidatedAlert]:
        """미청산 잔고를 '없어진 것'으로 기록하지 않고 비상 알림으로 남긴다."""
        ph = self.phase_at(now)
        if ph not in (ClosingPhase.AUCTION_DEFENSE, ClosingPhase.POST_CLOSE):
            return []
        out = []
        for t, q in daytrade_positions.items():
            if q > 0 and t not in overnight_approved:
                out.append(UnliquidatedAlert(t, q, ph, "EMERGENCY",
                                             "daytrade position not liquidated before closing auction; operator escalation"))
        return out
