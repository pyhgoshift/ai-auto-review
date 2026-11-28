"""투자 후보 스키마. 평가 불가 필드는 None + unverified_reasons[필드명]에 구체 사유를 강제한다."""
from __future__ import annotations

from datetime import date, datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator


class SourceRef(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)
    source_id: str                 # 예: "DART:rcept_no", "KRX:stat_id"
    url_or_id: str
    published_time: datetime
    retrieved_time: datetime
    payload_hash: str


class Catalyst(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)
    description: str
    status: Literal["CONFIRMED_DISCLOSURE", "ESTIMATE", "RUMOR"]
    expected_date: date | None
    sources: tuple[SourceRef, ...]

    @model_validator(mode="after")
    def _confirmed_needs_source(self) -> "Catalyst":
        if self.status == "CONFIRMED_DISCLOSURE" and not self.sources:
            raise ValueError("confirmed catalyst requires at least one source")
        return self


class Scenario(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)
    narrative: str
    probability: float | None = Field(default=None, ge=0, le=1)
    return_pct: float | None


_NULLABLE = (
    "intrinsic_value_range", "market_price_at_analysis", "free_float_shares", "average_daily_trading_value",
    "foreign_ownership", "foreign_ownership_headroom", "major_shareholder_ownership", "institutional_flow",
    "foreign_flow", "execution_cost_estimate_bps", "net_expected_value_bps",
)


class Candidate(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    candidate_id: str
    ticker: str
    company_name: str
    market: Literal["KOSPI", "KOSDAQ"]
    industry: str
    research_timestamp: datetime
    source_manifest: tuple[SourceRef, ...]
    edge_type: tuple[Literal["FUNDAMENTAL", "MICROSTRUCTURE", "EXECUTION"], ...]
    fundamental_thesis: str
    mispricing_hypothesis: str               # 누가, 왜 잘못 평가하는가
    resolution_mechanism: str                # 무엇이 괴리를 해소하는가
    priced_in_check: str                     # 이미 반영되었는가에 대한 근거
    confirmed_catalysts: tuple[Catalyst, ...]
    unconfirmed_catalysts: tuple[Catalyst, ...]
    catalyst_dates: tuple[date, ...]
    valuation_method: str
    intrinsic_value_range: tuple[float, float] | None
    market_price_at_analysis: float | None
    free_float_shares: int | None
    average_daily_trading_value: float | None
    profitability_metrics: dict[str, float | None]
    leverage_metrics: dict[str, float | None]
    foreign_ownership: float | None
    foreign_ownership_headroom: float | None
    major_shareholder_ownership: float | None
    institutional_flow: float | None
    foreign_flow: float | None
    bull_case: Scenario
    base_case: Scenario
    bear_case: Scenario
    invalidation_conditions: tuple[str, ...]
    expected_holding_period: str
    entry_conditions: tuple[str, ...]
    exit_conditions: tuple[str, ...]
    liquidity_constraints: str
    execution_cost_estimate_bps: float | None
    net_expected_value_bps: float | None
    evidence_quality: Literal["HIGH", "MEDIUM", "LOW", "INSUFFICIENT"]
    research_confidence: float | None = Field(default=None, ge=0, le=1)
    data_quality_status: Literal["VALID", "SUSPECT", "INVALID", "UNVERIFIED"]
    research_version: str
    unverified_reasons: dict[str, str] = Field(default_factory=dict)

    @model_validator(mode="after")
    def _nulls_need_reasons(self) -> "Candidate":
        missing = [f for f in _NULLABLE if getattr(self, f) is None and not self.unverified_reasons.get(f)]
        if missing:
            raise ValueError(f"null fields without unverified reason: {missing}")
        if not self.invalidation_conditions:
            raise ValueError("invalidation_conditions required")
        if any(c.status != "CONFIRMED_DISCLOSURE" for c in self.confirmed_catalysts):
            raise ValueError("confirmed_catalysts may only contain CONFIRMED_DISCLOSURE")
        return self

    def eligibility(self) -> tuple[bool, list[str]]:
        """[최종 적격 여부] — 결정론적 판단. 기대수익만으로 선정하지 않는다."""
        r: list[str] = []
        if self.data_quality_status != "VALID":
            r.append("data_quality_not_valid")
        if self.evidence_quality in ("LOW", "INSUFFICIENT"):
            r.append("evidence_quality_low")
        if self.net_expected_value_bps is None or self.net_expected_value_bps <= 0:
            r.append("net_ev_not_positive_or_unknown")
        if self.average_daily_trading_value is None:
            r.append("liquidity_unknown")
        if not self.source_manifest:
            r.append("no_sources")
        if not self.edge_type:
            r.append("edge_type_undeclared")
        return (not r, r)
