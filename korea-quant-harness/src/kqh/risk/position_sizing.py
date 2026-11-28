"""Fractional Kelly(상한 Quarter Kelly). p, b는 '전략 단위' 실증 통계이며 개별 신호의 확률이 아니다."""
from __future__ import annotations

from dataclasses import dataclass

from ..execution.transaction_cost import conservative_p


@dataclass(frozen=True)
class StrategyStats:
    strategy_id: str
    n_trades: int
    win_rate: float              # 동일 전략·체제·비용조건, 비용 차감 결과 정의
    avg_net_win: float           # 비용 차감 평균 이익 (금액 또는 bps, avg_net_loss와 같은 단위)
    avg_net_loss: float          # 양수
    calibration_valid: bool
    heavy_left_tail: bool        # 손실 꼬리 과대 → 이항 Kelly 부적합
    data_quality_valid: bool


@dataclass(frozen=True)
class KellyResult:
    fraction: float
    f_full: float
    reason: str


def kelly_fraction(s: StrategyStats, *, cap: float, min_trades: int, risk_blocked: bool) -> KellyResult:
    if risk_blocked:
        return KellyResult(0.0, 0.0, "risk_engine_blocked")
    if not s.calibration_valid:
        return KellyResult(0.0, 0.0, "calibration_invalid")
    if not s.data_quality_valid:
        return KellyResult(0.0, 0.0, "data_quality_invalid")
    if s.n_trades < min_trades:
        return KellyResult(0.0, 0.0, "insufficient_samples")
    if s.avg_net_loss <= 0 or s.avg_net_win <= 0:
        return KellyResult(0.0, 0.0, "payoff_ratio_unestimable")
    if s.heavy_left_tail:
        return KellyResult(0.0, 0.0, "non_binary_payoff_use_distributional_method")
    p = conservative_p(s.win_rate, s.n_trades)     # 추정오차 반영: 승률 하한 사용
    b = s.avg_net_win / s.avg_net_loss
    ev = p * s.avg_net_win - (1 - p) * s.avg_net_loss
    if ev <= 0:
        return KellyResult(0.0, 0.0, "non_positive_net_ev")
    f_full = max(0.0, p - (1 - p) / b)
    return KellyResult(min(cap, 0.25) * f_full, f_full, "ok")
