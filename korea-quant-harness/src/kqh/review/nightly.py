"""야간 평가. 매매하지 않은 신호도 동일 기준으로 평가해 선택 편향을 통제한다.

입력: SIGNAL_OUTCOME 이벤트 {signal_id, regime, predicted_prob, outcome(0/1), traded(bool), net_pnl(optional)}
outcome은 사전에 고정된 예측기간·결과 정의로 '사후' 라벨링된 값이어야 한다.
"""
from __future__ import annotations

from collections import defaultdict
from datetime import datetime

from ..data.event_store import EventStore
from .brier import brier_score, expected_calibration_error, reliability_bins


def build_reports(store: EventStore, as_of: datetime) -> dict:
    recs = [r["data"] for r in store.read("SIGNAL_OUTCOME")]
    out: dict = {"as_of": as_of.isoformat(), "n_signals": len(recs)}
    if not recs:
        out["CALIBRATION_REPORT"] = {"status": "NO_DATA"}
        return out
    p = [float(r["predicted_prob"]) for r in recs]
    y = [int(r["outcome"]) for r in recs]
    by_regime: dict[str, tuple[list[float], list[int]]] = defaultdict(lambda: ([], []))
    for r in recs:
        by_regime[r["regime"]][0].append(float(r["predicted_prob"]))
        by_regime[r["regime"]][1].append(int(r["outcome"]))
    traded = [r for r in recs if r.get("traded")]
    pnl = [float(r["net_pnl"]) for r in traded if r.get("net_pnl") is not None]
    out["CALIBRATION_REPORT"] = {
        "brier_all": brier_score(p, y), "ece_all": expected_calibration_error(p, y),
        "reliability": reliability_bins(p, y),
        "brier_by_regime": {k: brier_score(*v) for k, v in by_regime.items()},
        "n_traded": len(traded), "n_untraded": len(recs) - len(traded),
    }
    out["PNL_REPORT"] = {  # 확률 정확도와 손익은 별도 지표
        "n": len(pnl), "sum_net_pnl": sum(pnl) if pnl else None,
        "win_rate": (sum(1 for x in pnl if x > 0) / len(pnl)) if pnl else None,
    }
    out["PROPOSED_IMPROVEMENTS"] = {"auto_deploy": False, "note": "제안은 improvement_proposal.can_deploy 통과 필요"}
    return out
