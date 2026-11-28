"""SYSTEM 2(BRAIN) 어댑터 — [연동 미구현].

설계 결정
- 모델 ID는 설정의 brain.model_id(운영자가 Models API로 확인한 값)만 사용. null이면 호출 금지.
- 출력은 구조화 출력(JSON 스키마) → 우리 Pydantic 스키마(Candidate 등)로 재검증. 실패 시 폐기.
- 공시/뉴스 본문은 UntrustedText.for_prompt()로 감싸 데이터로만 전달.
- BRAIN 출력에는 주문·수량·한도 필드가 존재하지 않으며(extra=forbid), 어떤 출력도 OMS에 직접 닿지 않는다.
- 일일 비용 상한(brain.max_daily_cost_usd)이 null이면 호출 금지.
"""
from __future__ import annotations

from pydantic import BaseModel, ConfigDict

from ..errors import NotVerifiedError


class BrainConfig(BaseModel):
    model_config = ConfigDict(extra="forbid")
    model_id: str | None
    model_verified_at: str | None
    max_daily_cost_usd: float | None


class Brain:
    def __init__(self, cfg: BrainConfig):
        if not cfg.model_id or not cfg.model_verified_at or cfg.max_daily_cost_usd is None:
            raise NotVerifiedError("BRAIN disabled: model_id/verification/cost budget not set by operator")
        self.cfg = cfg

    async def analyze(self, *_args, **_kw):
        raise NotImplementedError("BRAIN API integration is gated (GATE 3)")
