"""확률 보정 지표. ACTUAL_OUTCOME은 사전에 고정된 예측기간·결과 정의로 기록된 0/1."""
from __future__ import annotations

from collections.abc import Sequence


def _check(p: Sequence[float], y: Sequence[int]) -> None:
    if len(p) != len(y) or not p:
        raise ValueError("probabilities and outcomes must be non-empty and equal length")
    if any(not 0.0 <= x <= 1.0 for x in p):
        raise ValueError("probabilities must be in [0,1]")
    if any(v not in (0, 1) for v in y):
        raise ValueError("outcomes must be 0/1")


def brier_score(p: Sequence[float], y: Sequence[int]) -> float:
    _check(p, y)
    return sum((a - b) ** 2 for a, b in zip(p, y)) / len(p)


def reliability_bins(p: Sequence[float], y: Sequence[int], n_bins: int = 10) -> list[dict]:
    _check(p, y)
    bins: list[list[tuple[float, int]]] = [[] for _ in range(n_bins)]
    for a, b in zip(p, y):
        bins[min(int(a * n_bins), n_bins - 1)].append((a, b))
    out = []
    for i, bb in enumerate(bins):
        if bb:
            out.append({"bin": i, "lo": i / n_bins, "hi": (i + 1) / n_bins, "n": len(bb),
                        "mean_pred": sum(x for x, _ in bb) / len(bb),
                        "freq": sum(v for _, v in bb) / len(bb)})
    return out


def expected_calibration_error(p: Sequence[float], y: Sequence[int], n_bins: int = 10) -> float:
    n = len(p)
    return sum(b["n"] / n * abs(b["mean_pred"] - b["freq"]) for b in reliability_bins(p, y, n_bins))
