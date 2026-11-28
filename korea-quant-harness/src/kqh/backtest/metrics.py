"""성과 지표(순수 함수). 수익률 시계열은 비용 차감 후 값이어야 한다."""
from __future__ import annotations

import math
from collections.abc import Sequence


def max_drawdown(equity: Sequence[float]) -> float:
    peak, mdd = -math.inf, 0.0
    for e in equity:
        peak = max(peak, e)
        mdd = min(mdd, e / peak - 1)
    return mdd


def sharpe(returns: Sequence[float], periods_per_year: int) -> float | None:
    if len(returns) < 2:
        return None
    m = sum(returns) / len(returns)
    sd = math.sqrt(sum((r - m) ** 2 for r in returns) / (len(returns) - 1))
    return None if sd == 0 else m / sd * math.sqrt(periods_per_year)


def profit_factor(trade_pnls: Sequence[float]) -> float | None:
    g = sum(x for x in trade_pnls if x > 0)
    l = -sum(x for x in trade_pnls if x < 0)
    return None if l == 0 else g / l


def max_consecutive_losses(trade_pnls: Sequence[float]) -> int:
    best = cur = 0
    for x in trade_pnls:
        cur = cur + 1 if x < 0 else 0
        best = max(best, cur)
    return best
