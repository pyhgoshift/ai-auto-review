"""확률 보정 상태. 모델의 자기평가 점수는 보정 근거가 아니다.

calibration_valid는 '사전에 고정된 결과 정의'에 대한 out-of-sample 표본에서
충분한 표본 수와 ECE 상한을 만족할 때만 True가 된다.
"""
from __future__ import annotations

from collections.abc import Sequence
from datetime import datetime

from pydantic import BaseModel, ConfigDict

from ..review.brier import brier_score, expected_calibration_error


class CalibrationStatus(BaseModel):
    model_config = ConfigDict(frozen=True)
    model_version: str
    outcome_definition: str           # 예: "120초 내 비용차감 수익>0"
    n_samples: int
    brier: float | None
    ece: float | None
    evaluated_at: datetime
    valid: bool
    reason: str


def evaluate_calibration(model_version: str, outcome_definition: str, probs: Sequence[float],
                         outcomes: Sequence[int], *, min_samples: int, max_ece: float,
                         evaluated_at: datetime, out_of_sample: bool) -> CalibrationStatus:
    n = len(probs)
    if n != len(outcomes):
        raise ValueError("length mismatch")
    if not out_of_sample:
        return CalibrationStatus(model_version=model_version, outcome_definition=outcome_definition, n_samples=n,
                                 brier=None, ece=None, evaluated_at=evaluated_at, valid=False,
                                 reason="in_sample_data_not_accepted")
    if n < min_samples:
        return CalibrationStatus(model_version=model_version, outcome_definition=outcome_definition, n_samples=n,
                                 brier=None, ece=None, evaluated_at=evaluated_at, valid=False,
                                 reason="insufficient_samples")
    b = brier_score(probs, outcomes)
    e = expected_calibration_error(probs, outcomes)
    ok = e <= max_ece
    return CalibrationStatus(model_version=model_version, outcome_definition=outcome_definition, n_samples=n,
                             brier=b, ece=e, evaluated_at=evaluated_at, valid=ok,
                             reason="ok" if ok else "ece_above_limit")
