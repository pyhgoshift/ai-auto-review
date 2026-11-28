"""거래비용 엔진. 모든 값은 bps(1/10000)로 계산. 순(net) 방식과 총(gross) 방식을 혼용하지 않는다:
이 모듈은 '총 기대수익 - 명시적 비용'만 사용한다."""
from __future__ import annotations

import math
from dataclasses import dataclass

from ..config import load_yaml
from ..domain import Market


@dataclass(frozen=True)
class CostModel:
    verified: bool
    commission_bps: float
    sell_tax_bps: dict[str, float]
    impact_coefficient: float

    @classmethod
    def from_file(cls, path: str) -> "CostModel":
        r = load_yaml(path)
        return cls(bool(r["verified"]), float(r["commission_bps"]),
                   {k: float(v) for k, v in r["sell_tax_bps"].items()}, float(r["impact_coefficient"]))

    def round_trip_bps(self, market: Market, spread_bps: float, order_value: float, adv_value: float,
                       extra_slippage_bps: float = 0.0) -> float:
        """왕복비용 = 매수·매도 수수료 + 매도세 + 스프레드 전체(진입 반 + 청산 반) + 시장충격×2 + 추가 슬리피지."""
        if spread_bps < 0 or order_value <= 0:
            raise ValueError("invalid cost inputs")
        if adv_value <= 0:
            return math.inf  # 유동성 정보 없음 → 비용 무한대 → 진입 불가
        impact = self.impact_coefficient * math.sqrt(order_value / adv_value) * 10000
        return (2 * self.commission_bps + self.sell_tax_bps[market.value] + spread_bps
                + 2 * impact + extra_slippage_bps)


def net_ev_bps(p_win: float, avg_gross_win_bps: float, avg_gross_loss_bps: float, round_trip_cost_bps: float) -> float:
    """총손익 기준 EV에서 비용을 명시적으로 1회 차감: EV = p·W - (1-p)·L - C."""
    if not 0 <= p_win <= 1 or avg_gross_win_bps < 0 or avg_gross_loss_bps < 0:
        raise ValueError("invalid EV inputs")
    return p_win * avg_gross_win_bps - (1 - p_win) * avg_gross_loss_bps - round_trip_cost_bps


def conservative_p(p_hat: float, n: int, z: float = 1.645) -> float:
    """승률 추정 불확실성을 반영한 Wilson 하한(단측 95%)."""
    if n <= 0:
        return 0.0
    den = 1 + z * z / n
    centre = p_hat + z * z / (2 * n)
    adj = z * math.sqrt(p_hat * (1 - p_hat) / n + z * z / (4 * n * n))
    return max(0.0, (centre - adj) / den)
