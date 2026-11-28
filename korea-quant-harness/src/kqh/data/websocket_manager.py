"""WebSocket 연결 관리(전송 계층 독립). 제한된 지수 백오프 재연결 + 누락 구간 복구 훅.

무제한 재시도 금지: max_attempts 초과 시 FAILED로 남고 운영자 알림 대상이 된다.
실제 KIS 전송 구현은 미검증 상태이므로 transport는 주입한다.
"""
from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from enum import StrEnum


class ConnState(StrEnum):
    CONNECTED = "CONNECTED"
    RECONNECTING = "RECONNECTING"
    FAILED = "FAILED"


@dataclass
class ReconnectPolicy:
    max_attempts: int = 5
    base_delay_s: float = 0.5
    max_delay_s: float = 30.0

    def delay(self, attempt: int) -> float:
        return min(self.max_delay_s, self.base_delay_s * 2 ** attempt)


@dataclass
class WebSocketManager:
    connect: Callable[[], Awaitable[None]]
    on_disconnected: Callable[[], Awaitable[None]]
    recover_gap: Callable[[], Awaitable[bool]]          # REST 재조회 등 — 성공 '확인' 시 True
    on_recovered: Callable[[], Awaitable[None]]
    policy: ReconnectPolicy = field(default_factory=ReconnectPolicy)
    sleep: Callable[[float], Awaitable[None]] = asyncio.sleep
    state: ConnState = ConnState.RECONNECTING
    attempts_log: list[str] = field(default_factory=list)

    async def handle_disconnect(self) -> ConnState:
        await self.on_disconnected()
        self.state = ConnState.RECONNECTING
        for attempt in range(self.policy.max_attempts):
            try:
                await self.connect()
            except (OSError, ConnectionError, TimeoutError) as exc:
                self.attempts_log.append(f"attempt{attempt}:{type(exc).__name__}")
                await self.sleep(self.policy.delay(attempt))
                continue
            if await self.recover_gap():
                await self.on_recovered()
                self.state = ConnState.CONNECTED
            else:
                self.state = ConnState.CONNECTED  # 연결은 됐지만 gap은 미복구 상태로 남음(진입 차단 유지)
                self.attempts_log.append("gap_not_recovered")
            return self.state
        self.state = ConnState.FAILED
        return self.state
