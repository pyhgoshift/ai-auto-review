"""통제된 자가 개선: 제안은 자동 생성·시험 가능하나, 배포는 운영자 승인 + 해시 일치가 필요하다."""
from __future__ import annotations

import hashlib
import json
from datetime import datetime
from enum import StrEnum
from pathlib import Path

from pydantic import BaseModel, ConfigDict

STAGES = ("ANALYZE", "PROPOSE", "VERSION", "BACKTEST", "UNIT_TEST", "INTEGRATION_TEST",
          "SHADOW_TEST", "RISK_REVIEW", "OPERATOR_APPROVAL")


class StageResult(StrEnum):
    PASS = "PASS"
    FAIL = "FAIL"
    NOT_RUN = "NOT_RUN"


class Proposal(BaseModel):
    model_config = ConfigDict(extra="forbid")
    proposal_id: str
    description: str
    artifact_hash: str                    # 모델·코드·설정 묶음의 sha256
    stages: dict[str, StageResult]
    expands_risk_limits: bool
    n_variants_tried: int                 # 다중비교 보정용 시도 횟수 기록
    created_at: datetime


class ApprovalRecord(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)
    proposal_id: str
    artifact_hash: str
    operator_id: str
    approved_at: datetime
    scope: str                            # "PAPER" | "LIVE"
    risk_limit_expansion_approved: bool = False


def artifact_hash(paths: list[Path]) -> str:
    h = hashlib.sha256()
    for p in sorted(paths):
        h.update(str(p).encode())
        h.update(p.read_bytes())
    return h.hexdigest()


def can_deploy(p: Proposal, approval: ApprovalRecord | None, target: str) -> tuple[bool, list[str]]:
    r: list[str] = []
    for s in STAGES[:-1]:
        if p.stages.get(s) != StageResult.PASS:
            r.append(f"stage_{s.lower()}_{p.stages.get(s, StageResult.NOT_RUN).value.lower()}")
    if approval is None:
        r.append("no_operator_approval")
    else:
        if approval.proposal_id != p.proposal_id:
            r.append("approval_for_different_proposal")
        if approval.artifact_hash != p.artifact_hash:
            r.append("artifact_hash_mismatch")
        if approval.scope != target:
            r.append(f"approval_scope_{approval.scope}_not_{target}")
        if p.expands_risk_limits and not approval.risk_limit_expansion_approved:
            r.append("risk_limit_expansion_not_approved")
    return (not r, r)


def load_approval(path: Path) -> ApprovalRecord | None:
    if not path.exists():
        return None
    return ApprovalRecord.model_validate(json.loads(path.read_text(encoding="utf-8")))
