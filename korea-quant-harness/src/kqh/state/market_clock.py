"""설정 가능한 거래 캘린더. 캘린더가 모르는 날짜/시각은 UNKNOWN으로 처리(신규 진입 차단)."""
from __future__ import annotations

from dataclasses import dataclass
from datetime import date, datetime, time, timedelta
from enum import StrEnum
from pathlib import Path

from ..config import load_yaml
from ..errors import ConfigError
from ..domain import KST, Venue, to_kst


class SessionName(StrEnum):
    CLOSED = "CLOSED"
    UNKNOWN = "UNKNOWN"
    PRE_OPEN_AUCTION = "PRE_OPEN_AUCTION"
    REGULAR = "REGULAR"
    CLOSING_AUCTION = "CLOSING_AUCTION"
    AFTER_MARKET = "AFTER_MARKET"
    PRE_MARKET = "PRE_MARKET"
    MAIN_MARKET = "MAIN_MARKET"


@dataclass(frozen=True)
class SessionWindow:
    name: SessionName
    start: datetime
    end: datetime
    continuous: bool


@dataclass(frozen=True)
class SessionState:
    venue: Venue
    session: SessionName
    continuous: bool
    reason: str

    @property
    def allows_continuous_entry(self) -> bool:
        return self.continuous and self.session not in (SessionName.CLOSED, SessionName.UNKNOWN)


def _parse_t(s: str) -> time:
    parts = [int(x) for x in s.split(":")]
    return time(*parts)


class TradingCalendar:
    def __init__(self, raw: dict):
        try:
            self.verified: bool = bool(raw["verified"])
            cov = raw["coverage"]
            self.cov_from = date.fromisoformat(cov["from"])
            self.cov_to = date.fromisoformat(cov["to"])
            self.holidays = {date.fromisoformat(d) for d in raw.get("holidays", [])}
            self.special = {date.fromisoformat(k): v for k, v in (raw.get("special_days") or {}).items()}
            self.sessions = raw["sessions"]
            cg = raw["closing_gate"]
            self.entry_block_offset = timedelta(minutes=int(cg["entry_block_offset_min"]))
            self.block_all_new_entries = bool(cg["block_all_new_entries"])
        except (KeyError, TypeError, ValueError) as exc:
            raise ConfigError(f"invalid market calendar: {exc}") from exc

    @classmethod
    def from_file(cls, path: str | Path) -> "TradingCalendar":
        return cls(load_yaml(path))

    def is_covered(self, d: date) -> bool:
        return self.cov_from <= d <= self.cov_to

    def is_trading_day(self, d: date) -> bool | None:
        """None = 캘린더 범위 밖(모름)."""
        if not self.is_covered(d):
            return None
        return d.weekday() < 5 and d not in self.holidays

    def windows(self, venue: Venue, d: date) -> list[SessionWindow]:
        shift = timedelta(minutes=int(self.special.get(d, {}).get("shift_minutes", 0)))
        out = []
        for s in self.sessions[venue.value]:
            st = datetime.combine(d, _parse_t(s["start"]), KST) + shift
            en = datetime.combine(d, _parse_t(s["end"]), KST) + shift
            out.append(SessionWindow(SessionName(s["name"]), st, en, bool(s["continuous"])))
        return out

    def session_at(self, venue: Venue, now: datetime) -> SessionState:
        k = to_kst(now)
        td = self.is_trading_day(k.date())
        if td is None:
            return SessionState(venue, SessionName.UNKNOWN, False, "date_outside_calendar_coverage")
        if not td:
            return SessionState(venue, SessionName.CLOSED, False, "non_trading_day")
        for w in self.windows(venue, k.date()):
            if w.start <= k < w.end:
                return SessionState(venue, w.name, w.continuous, "in_window")
        return SessionState(venue, SessionName.CLOSED, False, "outside_sessions")

    def closing_auction_start(self, d: date) -> datetime | None:
        if not self.is_trading_day(d):
            return None
        for w in self.windows(Venue.KRX, d):
            if w.name == SessionName.CLOSING_AUCTION:
                return w.start
        return None

    def regular_close(self, d: date) -> datetime | None:
        for w in self.windows(Venue.KRX, d):
            if w.name == SessionName.CLOSING_AUCTION:
                return w.end
        return None
