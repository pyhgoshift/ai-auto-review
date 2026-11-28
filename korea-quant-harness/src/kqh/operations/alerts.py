"""운영자 알림 싱크. 실제 채널(Slack/SMS 등) 연동은 미구현 — 현재는 로그 + 메모리 보관."""
from __future__ import annotations

import logging
from dataclasses import dataclass, field

log = logging.getLogger("kqh.alerts")


@dataclass
class AlertSink:
    sent: list[dict] = field(default_factory=list)

    def emergency(self, code: str, detail: str) -> None:
        self.sent.append({"severity": "EMERGENCY", "code": code, "detail": detail})
        log.critical("ALERT", extra={"code": code, "detail": detail})
