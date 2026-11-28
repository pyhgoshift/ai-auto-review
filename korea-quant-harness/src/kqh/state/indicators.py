"""단순·결정론적 지표. 표본 부족 시 None(값을 만들어내지 않음)."""
from __future__ import annotations

import math
from collections.abc import Sequence


def simple_returns(prices: Sequence[float]) -> list[float]:
    return [prices[i] / prices[i - 1] - 1 for i in range(1, len(prices)) if prices[i - 1] > 0]


def realized_vol(prices: Sequence[float], min_points: int = 10) -> float | None:
    """로그수익률 표본표준편차(구간 단위, 연환산 아님)."""
    if len(prices) < min_points or any(p <= 0 for p in prices):
        return None
    lr = [math.log(prices[i] / prices[i - 1]) for i in range(1, len(prices))]
    m = sum(lr) / len(lr)
    var = sum((x - m) ** 2 for x in lr) / (len(lr) - 1)
    return math.sqrt(var)


def window_return(prices: Sequence[float]) -> float | None:
    if len(prices) < 2 or prices[0] <= 0:
        return None
    return prices[-1] / prices[0] - 1


def drawdown_from_high(prices: Sequence[float]) -> float | None:
    if not prices:
        return None
    hi = max(prices)
    return prices[-1] / hi - 1 if hi > 0 else None


def trade_pressure(buy_volume: int, sell_volume: int) -> float | None:
    """체결 압력 = (매수체결 - 매도체결)/(합). 체결 방향 구분 근거는 데이터 원천 문서로 확인 필요."""
    tot = buy_volume + sell_volume
    return None if tot == 0 else (buy_volume - sell_volume) / tot
