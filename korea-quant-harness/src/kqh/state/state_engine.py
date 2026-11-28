"""종목별 결정론적 상태 엔진(asyncio).

- 구형 이벤트는 최신 상태를 덮어쓰지 않는다.
- WebSocket 단절 시 gap_unrecovered=True → 복구 확인 전까지 신규 진입 차단.
- readiness()가 하나라도 실패 사유를 반환하면 신규 진입 금지.
"""
from __future__ import annotations

import asyncio
import uuid
from dataclasses import dataclass, field
from datetime import datetime

from ..domain import Venue, ViState, require_aware
from .indicators import drawdown_from_high, realized_vol, trade_pressure, window_return
from .market_clock import SessionState
from .orderbook import BookUpdate, OrderBook
from .snapshot import StateSnapshot, default_expiry


@dataclass
class InstrumentState:
    ticker: str
    venue: Venue
    book: OrderBook = field(init=False)
    vi_state: ViState = ViState.UNKNOWN
    vi_released_at: datetime | None = None
    halted: bool | None = None            # None = 확인되지 않음
    restricted: bool | None = None        # 관리종목/투자경고 등. None = 미확인
    connected: bool = False
    gap_unrecovered: bool = True          # 시작 시점엔 초기 조회 전이므로 True
    account_synced: bool = False
    prices: list[float] = field(default_factory=list)
    buy_vol: int = 0
    sell_vol: int = 0
    last_received: datetime | None = None
    last_event: datetime | None = None
    position_qty: int = 0
    open_order_qty: int = 0

    def __post_init__(self) -> None:
        self.book = OrderBook(self.ticker, self.venue)


class StateEngine:
    def __init__(self, max_staleness_ms: int, vi_stabilization_sec: int, snapshot_ttl_ms: int):
        self.max_staleness_ms = max_staleness_ms
        self.vi_stab = vi_stabilization_sec
        self.ttl_ms = snapshot_ttl_ms
        self._states: dict[tuple[str, Venue], InstrumentState] = {}
        self._locks: dict[tuple[str, Venue], asyncio.Lock] = {}

    def state(self, ticker: str, venue: Venue) -> InstrumentState:
        key = (ticker, venue)
        if key not in self._states:
            self._states[key] = InstrumentState(ticker, venue)
            self._locks[key] = asyncio.Lock()
        return self._states[key]

    def _lock(self, ticker: str, venue: Venue) -> asyncio.Lock:
        self.state(ticker, venue)
        return self._locks[(ticker, venue)]

    # ---- 입력 핸들러 ----
    async def on_book(self, u: BookUpdate) -> bool:
        async with self._lock(u.ticker, u.venue):
            st = self.state(u.ticker, u.venue)
            applied = st.book.apply(u)
            if applied:
                st.last_received = u.received_time
                st.last_event = u.event_time
            return applied

    async def on_trade(self, ticker: str, venue: Venue, price: float, qty: int, aggressor_buy: bool | None,
                       received_time: datetime) -> None:
        async with self._lock(ticker, venue):
            st = self.state(ticker, venue)
            st.prices.append(price)
            st.prices = st.prices[-600:]
            if aggressor_buy is True:
                st.buy_vol += qty
            elif aggressor_buy is False:
                st.sell_vol += qty
            st.last_received = require_aware(received_time)

    async def on_vi(self, ticker: str, venue: Venue, vi: ViState, at: datetime) -> None:
        async with self._lock(ticker, venue):
            st = self.state(ticker, venue)
            if st.vi_state in (ViState.VI_ACTIVE, ViState.VI_REOPEN_PENDING) and vi == ViState.NORMAL:
                st.vi_released_at = require_aware(at)
            st.vi_state = vi

    async def on_status(self, ticker: str, venue: Venue, *, halted: bool | None = None,
                        restricted: bool | None = None) -> None:
        async with self._lock(ticker, venue):
            st = self.state(ticker, venue)
            if halted is not None:
                st.halted = halted
            if restricted is not None:
                st.restricted = restricted

    async def on_connection(self, ticker: str, venue: Venue, connected: bool) -> None:
        async with self._lock(ticker, venue):
            st = self.state(ticker, venue)
            st.connected = connected
            if not connected:
                st.gap_unrecovered = True

    async def on_gap_recovered(self, ticker: str, venue: Venue) -> None:
        """REST 재조회 등으로 누락 구간 복구가 '확인된' 경우에만 호출한다."""
        async with self._lock(ticker, venue):
            self.state(ticker, venue).gap_unrecovered = False

    async def on_account_sync(self, ticker: str, venue: Venue, position_qty: int, open_order_qty: int) -> None:
        async with self._lock(ticker, venue):
            st = self.state(ticker, venue)
            st.position_qty, st.open_order_qty, st.account_synced = position_qty, open_order_qty, True

    # ---- 판정 ----
    def readiness(self, ticker: str, venue: Venue, now: datetime, session: SessionState) -> list[str]:
        """신규 진입 차단 사유 목록. 빈 리스트일 때만 진입 후보가 될 수 있다."""
        st = self.state(ticker, venue)
        r: list[str] = []
        if not st.connected:
            r.append("feed_disconnected")
        if st.gap_unrecovered:
            r.append("data_gap_unrecovered")
        if st.last_received is None:
            r.append("no_data")
        else:
            lag = (now - st.last_received).total_seconds() * 1000
            if lag > self.max_staleness_ms:
                r.append("stale_data")
            if lag < -1000:
                r.append("clock_skew")
        r += [f"book_{i}" for i in st.book.validity_issues()]
        if st.vi_state != ViState.NORMAL:
            r.append(f"vi_{st.vi_state.value.lower()}")
        elif st.vi_released_at is not None and (now - st.vi_released_at).total_seconds() < self.vi_stab:
            r.append("vi_stabilization")
        if st.halted is not False:
            r.append("halted_or_unknown")
        if st.restricted is not False:
            r.append("restricted_or_unknown")
        if not st.account_synced:
            r.append("account_not_synced")
        if session.venue != venue or not session.allows_continuous_entry:
            r.append(f"session_{session.session.value.lower()}")
        return r

    def build_snapshot(self, ticker: str, venue: Venue, now: datetime, session: SessionState) -> StateSnapshot | None:
        st = self.state(ticker, venue)
        if self.readiness(ticker, venue, now, session):
            return None
        b = st.book
        obi, sp = b.obi(), b.spread_bps()
        if obi is None or sp is None:
            return None
        lag = int(max(0, (now - st.last_received).total_seconds() * 1000))
        return StateSnapshot(
            snapshot_id=str(uuid.uuid4()), ticker=ticker, venue=venue.value,
            snapshot_timestamp=now, expires_at=default_expiry(now, self.ttl_ms),
            session=session.session.value, vi_state=st.vi_state.value,
            best_bid=float(b.best_bid), best_ask=float(b.best_ask), mid=float(b.mid()),
            spread_bps=sp, obi10=obi, trade_pressure=trade_pressure(st.buy_vol, st.sell_vol),
            ret_1m=window_return(st.prices[-60:]), rv_5m=realized_vol(st.prices[-300:]),
            dd_from_high=drawdown_from_high(st.prices), data_lag_ms=lag,
            position_qty=st.position_qty, open_order_qty=st.open_order_qty,
            source_sequence=b.last.sequence,
        )
