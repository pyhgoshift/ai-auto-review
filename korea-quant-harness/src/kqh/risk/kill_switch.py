"""킬 스위치.

1. MANUAL_KILL       — 운영자 직접 작동(메서드 또는 외부 파일 touch)
2. AUTOMATIC_KILL    — 위험 한도 초과/장애
3. BROKER_SIDE_PROTECTION — [미검증] 증권사 측 기능. 지원 여부 확인 전까지 '사용 불가'로 간주.

주문 경로 밖 독립 통제: kill_file이 존재하면 프로세스 내부 상태와 무관하게 활성으로 판정.
자동 해제 없음. 해제는 운영자 ID + 승인 참조가 있어야 하며, 외부 파일이 남아 있으면 해제 불가.
"""
from __future__ import annotations

import logging
from dataclasses import dataclass, field
from datetime import datetime
from enum import StrEnum
from pathlib import Path

from ..domain import utcnow

log = logging.getLogger(__name__)


class KillType(StrEnum):
    MANUAL_KILL = "MANUAL_KILL"
    AUTOMATIC_KILL = "AUTOMATIC_KILL"
    BROKER_SIDE_PROTECTION = "BROKER_SIDE_PROTECTION"


@dataclass(frozen=True)
class KillEvent:
    kind: KillType
    reason: str
    at: datetime
    actor: str


@dataclass
class KillSwitch:
    kill_file: Path | None = None
    broker_side_supported: bool = False   # 공식 확인 전 False 고정
    _active: list[KillEvent] = field(default_factory=list)
    history: list[dict] = field(default_factory=list)

    def trigger(self, kind: KillType, reason: str, actor: str = "system") -> KillEvent:
        ev = KillEvent(kind, reason, utcnow(), actor)
        self._active.append(ev)
        self.history.append({"event": "KILL_TRIGGERED", "kind": kind.value, "reason": reason,
                             "actor": actor, "at": ev.at.isoformat()})
        log.critical("kill_switch_triggered", extra={"kind": kind.value, "reason": reason})
        return ev

    def is_active(self) -> bool:
        if self.kill_file is not None and self.kill_file.exists():
            return True
        return bool(self._active)

    def reasons(self) -> list[str]:
        r = [f"{e.kind.value}:{e.reason}" for e in self._active]
        if self.kill_file is not None and self.kill_file.exists():
            r.append(f"MANUAL_KILL:kill_file:{self.kill_file}")
        return r

    def release(self, operator_id: str, approval_ref: str) -> bool:
        if not operator_id or not approval_ref:
            raise PermissionError("release requires operator_id and approval_ref")
        if self.kill_file is not None and self.kill_file.exists():
            return False
        self.history.append({"event": "KILL_RELEASED", "operator": operator_id, "approval_ref": approval_ref,
                             "at": utcnow().isoformat(), "cleared": [e.reason for e in self._active]})
        self._active.clear()
        return True

    def broker_side_protect(self) -> None:
        if not self.broker_side_supported:
            raise NotImplementedError("BROKER_SIDE_PROTECTION not verified with broker documentation")
