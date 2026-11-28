"""데이터·모델 드리프트: PSI(Population Stability Index)."""
from __future__ import annotations

import math
from collections.abc import Sequence


def psi(expected: Sequence[float], actual: Sequence[float], n_bins: int = 10) -> float:
    if len(expected) < n_bins or len(actual) < n_bins:
        raise ValueError("insufficient samples for PSI")
    lo, hi = min(expected), max(expected)
    if hi == lo:
        raise ValueError("degenerate reference distribution")
    edges = [lo + (hi - lo) * i / n_bins for i in range(1, n_bins)]

    def hist(xs: Sequence[float]) -> list[float]:
        c = [0] * n_bins
        for x in xs:
            c[sum(1 for e in edges if x >= e)] += 1
        return [max(v / len(xs), 1e-6) for v in c]

    e, a = hist(expected), hist(actual)
    return sum((ai - ei) * math.log(ai / ei) for ei, ai in zip(e, a))
