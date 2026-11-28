"""공용 열거형과 시각 유틸리티."""
from __future__ import annotations

from datetime import datetime, timezone
from enum import StrEnum
from zoneinfo import ZoneInfo

KST = ZoneInfo("Asia/Seoul")


class Mode(StrEnum):
    PAPER = "PAPER"
    LIVE = "LIVE"


class Venue(StrEnum):
    KRX = "KRX"
    NXT = "NXT"


class Market(StrEnum):
    KOSPI = "KOSPI"
    KOSDAQ = "KOSDAQ"


class Side(StrEnum):
    BUY = "BUY"
    SELL = "SELL"


class IntentKind(StrEnum):
    """ENTRY는 위험 증가, REDUCE는 보유 감소(청산) 전용 안전 경로."""
    ENTRY = "ENTRY"
    REDUCE = "REDUCE"


class ViState(StrEnum):
    NORMAL = "NORMAL"
    VI_PENDING = "VI_PENDING"
    VI_ACTIVE = "VI_ACTIVE"
    VI_REOPEN_PENDING = "VI_REOPEN_PENDING"
    HALTED = "HALTED"
    UNKNOWN = "UNKNOWN"


def require_aware(dt: datetime, name: str = "datetime") -> datetime:
    if dt.tzinfo is None or dt.utcoffset() is None:
        raise ValueError(f"{name} must be timezone-aware")
    return dt


def to_kst(dt: datetime) -> datetime:
    return require_aware(dt).astimezone(KST)


def utcnow() -> datetime:
    return datetime.now(timezone.utc)
