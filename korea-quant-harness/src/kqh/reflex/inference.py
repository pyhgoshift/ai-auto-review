"""SYSTEM 1 비동기 추론 러너 + 결정론적 기준선.

- 동시성 제한(Semaphore)
- 지연시간 초과 응답 폐기
- 같은 종목에서 구형 스냅샷 판단이 최신 판단을 덮어쓰지 못함
"""
from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass, field
from datetime import datetime, timedelta

from ..state.snapshot import StateSnapshot
from .model_adapter import ReflexModel
from .schemas import ReflexSignal, SafeNeutral, parse_reflex_output


class DeterministicBaseline:
    """모델 없는 기준선. 보정되지 않았으므로 calibrated_prob는 0.5(중립)로 고정하여
    진입 게이트(>0.80)를 통과하지 못한다 — 보정 근거 없이 확률을 만들지 않는다."""
    version = "baseline-0.1.0"

    async def infer(self, s: StateSnapshot, candidate_id: str) -> str:
        long_bias = s.obi10 > 0.3 and (s.trade_pressure or 0) > 0.2 and s.spread_bps < 20
        return json.dumps({
            "regime": "trending" if (s.ret_1m or 0) > 0 else "mean_reverting",
            "direction": "long" if long_bias else "neutral",
            "toxic_flow": s.spread_bps > 30,
            "setup_quality": 2 if long_bias else 0,
            "calibrated_prob": 0.5,
            "risk_state": "safe",
            "candidate_id": candidate_id, "ticker": s.ticker, "snapshot_id": s.snapshot_id,
            "snapshot_timestamp": s.snapshot_timestamp.isoformat(),
            "model_version": self.version, "signal_version": "sig-0.1.0",
            "prediction_horizon_sec": 120,
            "signal_valid_until": (s.snapshot_timestamp + timedelta(seconds=2)).isoformat(),
            "reason_code": "OBI_PRESSURE" if long_bias else "NO_SETUP",
        })


@dataclass
class ReflexRunner:
    model: ReflexModel
    max_concurrency: int
    max_latency_ms: int
    _sem: asyncio.Semaphore = field(init=False)
    _latest: dict[str, tuple[datetime, ReflexSignal | SafeNeutral]] = field(default_factory=dict)

    def __post_init__(self) -> None:
        self._sem = asyncio.Semaphore(self.max_concurrency)

    async def evaluate(self, s: StateSnapshot, candidate_id: str) -> ReflexSignal | SafeNeutral:
        async with self._sem:
            try:
                raw = await asyncio.wait_for(self.model.infer(s, candidate_id), self.max_latency_ms / 1000)
            except TimeoutError:
                return SafeNeutral(reason_code="LATENCY_EXCEEDED", raw_excerpt="")
            except Exception as exc:  # 모델 장애 → 관망 (fail-closed)
                return SafeNeutral(reason_code=f"MODEL_ERROR:{type(exc).__name__}", raw_excerpt="")
        res = parse_reflex_output(raw)
        if isinstance(res, ReflexSignal) and (res.snapshot_id != s.snapshot_id or res.ticker != s.ticker):
            res = SafeNeutral(reason_code="SNAPSHOT_MISMATCH", raw_excerpt="")
        prev = self._latest.get(s.ticker)
        if prev is None or s.snapshot_timestamp > prev[0]:
            self._latest[s.ticker] = (s.snapshot_timestamp, res)
        return res

    def latest(self, ticker: str) -> ReflexSignal | SafeNeutral | None:
        v = self._latest.get(ticker)
        return v[1] if v else None
