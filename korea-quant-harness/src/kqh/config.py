"""설정 로딩. 위험 한도는 운영자 승인 파일에서만 읽으며, 누락 시 None → 신규 주문 금지."""
from __future__ import annotations

import logging
from pathlib import Path
from typing import Literal

import yaml
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from .errors import ConfigError

log = logging.getLogger(__name__)

ApprovalStatus = Literal["UNAPPROVED", "PAPER_APPROVED", "LIVE_APPROVED"]


class _Strict(BaseModel):
    # 기본값 없음 + extra 금지: 필드가 하나라도 빠지면 로딩 실패 → fail-closed
    model_config = ConfigDict(extra="forbid", frozen=True)


class Approval(_Strict):
    status: ApprovalStatus
    approved_by: str | None
    approved_at: str | None
    config_version: str


class AccountLimits(_Strict):
    max_daily_loss_krw: float = Field(gt=0)
    max_daily_drawdown_pct: float = Field(gt=0)
    max_cumulative_drawdown_pct: float = Field(gt=0)
    max_gross_exposure_krw: float = Field(gt=0)
    near_limit_utilization_pct: float = Field(gt=0, lt=100)


class PositionLimits(_Strict):
    max_position_weight_pct: float = Field(gt=0, le=100)
    max_sector_weight_pct: float = Field(gt=0, le=100)
    max_order_value_krw: float = Field(gt=0)
    max_adv_participation_pct: float = Field(gt=0, le=100)
    max_book_participation_pct: float = Field(gt=0, le=100)


class ExecutionLimits(_Strict):
    max_spread_bps: float = Field(gt=0)
    max_data_staleness_ms: int = Field(gt=0)
    max_api_latency_ms: int = Field(gt=0)
    vi_stabilization_sec: int = Field(ge=0)
    max_submit_retries: int = Field(ge=0, le=0)  # 재전송 금지를 스키마로 강제


class SignalLimits(_Strict):
    min_setup_quality: int = Field(ge=0, le=3)
    entry_prob_threshold: float = Field(gt=0, lt=1)
    escalation_prob_threshold: float = Field(gt=0, lt=1)
    min_net_ev_margin_bps: float = Field(ge=0)
    min_calibration_samples: int = Field(gt=0)
    max_calibration_ece: float = Field(gt=0, lt=1)


class SizingLimits(_Strict):
    kelly_fraction_cap: float = Field(gt=0, le=0.25)  # Quarter Kelly 초과 불가
    min_strategy_trades: int = Field(gt=0)


class RiskLimits(_Strict):
    approval: Approval
    account: AccountLimits
    position: PositionLimits
    execution: ExecutionLimits
    signal: SignalLimits
    sizing: SizingLimits


def load_yaml(path: str | Path) -> dict:
    p = Path(path)
    if not p.exists():
        raise ConfigError(f"config not found: {p}")
    with p.open(encoding="utf-8") as f:
        data = yaml.safe_load(f)
    if not isinstance(data, dict):
        raise ConfigError(f"config is not a mapping: {p}")
    return data


def load_risk_limits(path: str | Path) -> RiskLimits | None:
    """실패 시 예외 대신 None을 반환하고, 호출자는 None을 '거래 금지'로 해석한다."""
    try:
        return RiskLimits.model_validate(load_yaml(path))
    except (ConfigError, ValidationError, yaml.YAMLError) as exc:
        log.error("risk_limits_load_failed", extra={"path": str(path), "error": str(exc)})
        return None
