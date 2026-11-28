"""진입점: 모드 결정. 기본 PAPER. LIVE는 설정·승인 기록·확인 문자열이 모두 있어야 하며, 현재 LIVE 경로는 미구현."""
from __future__ import annotations

import os
from pathlib import Path

from .config import load_yaml
from .domain import Mode
from .errors import ConfigError, NotVerifiedError


def resolve_mode(base_dir: Path) -> Mode:
    requested = os.environ.get("KQH_MODE", "PAPER").upper()
    if requested != "LIVE":
        return Mode.PAPER
    live = load_yaml(base_dir / "configs" / "live.yaml")
    if not live.get("enabled"):
        raise ConfigError("LIVE requested but configs/live.yaml enabled=false")
    if not (base_dir / live["approval_record"]).exists():
        raise ConfigError("LIVE requested but GATE 6 approval record missing")
    if not os.environ.get("KQH_LIVE_CONFIRM"):
        raise ConfigError("LIVE requested without KQH_LIVE_CONFIRM")
    raise NotVerifiedError("LIVE execution path not implemented (broker adapter unverified)")
