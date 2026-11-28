"""SYSTEM 1 출력 스키마. 신호(ReflexSignal)와 주문 승인(EntryDecision/RiskApproval)은 별개 자료구조다."""
from __future__ import annotations

import json
import logging
from datetime import datetime
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError, model_validator

log = logging.getLogger(__name__)

Regime = Literal["trending", "mean_reverting", "high_vol", "policy_driven", "crisis"]


class ReflexSignal(BaseModel):
    """모델 출력. 수량·가격·주문 권한 필드는 존재하지 않으며 추가 필드는 거부된다."""
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    regime: Regime
    direction: Literal["long", "neutral"]
    toxic_flow: bool
    setup_quality: Annotated[int, Field(ge=0, le=3)]
    calibrated_prob: Annotated[float, Field(ge=0.0, le=1.0)]
    risk_state: Literal["safe", "near_limit", "reduce"]   # 모델 참고값일 뿐

    candidate_id: str
    ticker: str
    snapshot_id: str
    snapshot_timestamp: datetime
    model_version: str
    signal_version: str
    prediction_horizon_sec: Annotated[int, Field(gt=0)]
    signal_valid_until: datetime
    reason_code: str

    @model_validator(mode="after")
    def _times(self) -> "ReflexSignal":
        for n in ("snapshot_timestamp", "signal_valid_until"):
            v = getattr(self, n)
            if v.tzinfo is None:
                raise ValueError(f"{n} must be timezone-aware")
        if self.signal_valid_until <= self.snapshot_timestamp:
            raise ValueError("signal_valid_until must be after snapshot_timestamp")
        return self


class SafeNeutral(BaseModel):
    """파싱 실패/비정상 출력 시의 안전한 관망 결과. 주문 단계로 진행할 수 없다."""
    model_config = ConfigDict(frozen=True)
    direction: Literal["neutral"] = "neutral"
    reason_code: str
    raw_excerpt: str


def parse_reflex_output(raw: str | bytes) -> ReflexSignal | SafeNeutral:
    try:
        data = json.loads(raw)
        if not isinstance(data, dict):
            raise ValueError("not an object")
        # strict 모드에서 JSON의 datetime 문자열을 허용하기 위해 json 모드로 검증
        return ReflexSignal.model_validate_json(json.dumps(data))
    except (ValueError, ValidationError, TypeError) as exc:
        text = raw.decode("utf-8", "replace") if isinstance(raw, bytes) else str(raw)
        log.warning("reflex_output_rejected", extra={"error": str(exc)[:200]})
        return SafeNeutral(reason_code="INVALID_MODEL_OUTPUT", raw_excerpt=text[:120])


class ConditionResult(BaseModel):
    model_config = ConfigDict(frozen=True)
    name: str
    passed: bool
    observed: str
    required: str


class EntryDecision(BaseModel):
    """결정론적 진입 게이트 결과(기계 판독 가능). approved여도 주문 승인 아님 → RiskEngine 필요."""
    model_config = ConfigDict(frozen=True)
    signal_id: str
    approved: bool
    escalate_to_system2: bool
    conditions: tuple[ConditionResult, ...]

    @property
    def blocked_by(self) -> list[str]:
        return [c.name for c in self.conditions if not c.passed]
