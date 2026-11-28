"""SYSTEM 1 입력용 고정 스키마 스냅샷(숫자 중심, 400토큰 미만).

원본 호가는 이벤트 저장소에 별도 보존하며, 스냅샷은 그 요약일 뿐이다.
"""
from __future__ import annotations

import json
from datetime import datetime, timedelta
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

MAX_MODEL_INPUT_TOKENS = 400


class StateSnapshot(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    snapshot_id: str
    ticker: str
    venue: Literal["KRX", "NXT"]
    snapshot_timestamp: datetime
    expires_at: datetime
    session: str
    vi_state: str
    best_bid: float
    best_ask: float
    mid: float
    spread_bps: float
    obi10: float
    trade_pressure: float | None
    ret_1m: float | None
    rv_5m: float | None
    dd_from_high: float | None
    data_lag_ms: int = Field(ge=0)
    position_qty: int = Field(ge=0)
    open_order_qty: int = Field(ge=0)
    source_sequence: int

    def to_model_input(self) -> str:
        """짧은 키의 압축 JSON. 텍스트 필드(뉴스 등)는 절대 넣지 않는다."""
        d = {
            "id": self.snapshot_id, "t": self.ticker, "v": self.venue,
            "ts": self.snapshot_timestamp.isoformat(timespec="milliseconds"),
            "ss": self.session, "vi": self.vi_state,
            "bb": self.best_bid, "ba": self.best_ask, "sp": round(self.spread_bps, 2),
            "obi": round(self.obi10, 4),
            "tp": None if self.trade_pressure is None else round(self.trade_pressure, 4),
            "r1": None if self.ret_1m is None else round(self.ret_1m, 5),
            "rv": None if self.rv_5m is None else round(self.rv_5m, 5),
            "dd": None if self.dd_from_high is None else round(self.dd_from_high, 5),
            "lag": self.data_lag_ms, "pos": self.position_qty, "oo": self.open_order_qty,
        }
        return json.dumps(d, separators=(",", ":"), ensure_ascii=True)


def estimate_tokens(text: str) -> int:
    """보수적 상한 추정(문자 2.5개당 1토큰). 실제 토크나이저로 재확인 전까지는 이 상한만 신뢰."""
    return int(len(text) / 2.5) + 1


def assert_within_budget(s: StateSnapshot) -> str:
    text = s.to_model_input()
    n = estimate_tokens(text)
    if n >= MAX_MODEL_INPUT_TOKENS:
        raise ValueError(f"snapshot too large: ~{n} tokens")
    return text


def default_expiry(ts: datetime, ttl_ms: int) -> datetime:
    return ts + timedelta(milliseconds=ttl_ms)
