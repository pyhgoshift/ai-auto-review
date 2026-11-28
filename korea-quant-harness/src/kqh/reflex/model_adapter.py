"""SYSTEM 1 모델 어댑터 프로토콜. 어댑터는 문자열(JSON)만 반환하며 주문 객체에 접근할 수 없다."""
from __future__ import annotations

from typing import Protocol

from ..state.snapshot import StateSnapshot


class ReflexModel(Protocol):
    version: str

    async def infer(self, snapshot: StateSnapshot, candidate_id: str) -> str: ...


class OnnxReflexModel:
    """[미구현] ONNX/로컬 SLM. 기준선 대비 out-of-sample 개선이 입증되기 전 사용 금지."""
    version = "onnx-unimplemented"

    async def infer(self, snapshot: StateSnapshot, candidate_id: str) -> str:
        raise NotImplementedError("ONNX reflex model not implemented (GATE 5 evidence required)")
