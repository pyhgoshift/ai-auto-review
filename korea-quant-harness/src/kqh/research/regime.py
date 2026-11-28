"""시장 체제. AI 판단은 권고이며, 강제 신규 진입 금지는 아래 결정론적 함수가 결정한다."""
from __future__ import annotations

from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict

RegimeLabel = Literal["trending", "mean_reverting", "high_vol", "policy_driven", "crisis", "UNVERIFIED"]


class RegimeAssessment(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)
    regime: RegimeLabel
    regime_confidence: float | None
    observed_at: datetime
    data_freshness_sec: float | None
    regime_change_reason: str
    supporting_evidence: tuple[str, ...]
    contradicting_evidence: tuple[str, ...]


class HardMarketInputs(BaseModel):
    """[임계값 예시 - 백테스트 검증 필요] 위기/장애 판단용 숫자 입력."""
    model_config = ConfigDict(frozen=True)
    index_drawdown_intraday_pct: float | None
    circuit_breaker_active: bool | None
    market_data_feed_ok: bool
    market_wide_spread_bps_median: float | None


def hard_entry_block(r: RegimeAssessment | None, m: HardMarketInputs, *, crisis_dd_pct: float,
                     max_market_spread_bps: float, max_staleness_sec: float) -> list[str]:
    reasons: list[str] = []
    if r is None or r.regime == "UNVERIFIED":
        reasons.append("regime_unverified")
    elif r.regime == "crisis":
        reasons.append("regime_crisis")
    if r is not None and (r.data_freshness_sec is None or r.data_freshness_sec > max_staleness_sec):
        reasons.append("regime_data_stale")
    if not m.market_data_feed_ok:
        reasons.append("market_feed_down")
    if m.circuit_breaker_active is not False:
        reasons.append("circuit_breaker_active_or_unknown")
    if m.index_drawdown_intraday_pct is None or m.index_drawdown_intraday_pct <= -abs(crisis_dd_pct):
        reasons.append("index_crash_or_unknown")
    if m.market_wide_spread_bps_median is None or m.market_wide_spread_bps_median > max_market_spread_bps:
        reasons.append("liquidity_collapse_or_unknown")
    return reasons
