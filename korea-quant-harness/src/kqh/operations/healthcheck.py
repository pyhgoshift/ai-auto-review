"""헬스체크: 시계 오차·저장소 쓰기. 실패는 RiskEngine 플래그로 전달된다."""
from __future__ import annotations

from datetime import datetime
from pathlib import Path


def clock_skew_ok(local: datetime, reference: datetime, max_skew_ms: int) -> bool:
    """reference는 신뢰 가능한 시각원(NTP/거래소 시각)에서 얻어야 한다 — 연동 미구현."""
    return abs((local - reference).total_seconds() * 1000) <= max_skew_ms


def storage_writable(dir_: Path) -> bool:
    try:
        dir_.mkdir(parents=True, exist_ok=True)
        p = dir_ / ".healthcheck"
        p.write_text("ok")
        p.unlink()
        return True
    except OSError:
        return False
