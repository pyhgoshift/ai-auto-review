"""상태 엔진·호가(T07–T09) 및 계산 규칙."""
import asyncio
from datetime import timedelta
from decimal import Decimal

from conftest import NOW
from kqh.domain import Venue, ViState
from kqh.state.market_clock import SessionName, SessionState
from kqh.state.orderbook import BookUpdate, Level, OrderBook
from kqh.state.snapshot import assert_within_budget
from kqh.state.state_engine import StateEngine

REG = SessionState(Venue.KRX, SessionName.REGULAR, True, "test")


def book(seq: int, t=NOW, bid="69900", ask="70000", bq=500, aq=300) -> BookUpdate:
    bids = tuple(Level(Decimal(bid) - 100 * i, bq) for i in range(10))
    asks = tuple(Level(Decimal(ask) + 100 * i, aq) for i in range(10))
    return BookUpdate("005930", Venue.KRX, seq, t, t, bids, asks)


async def ready_engine() -> StateEngine:
    se = StateEngine(max_staleness_ms=2000, vi_stabilization_sec=120, snapshot_ttl_ms=1500)
    await se.on_connection("005930", Venue.KRX, True)
    await se.on_gap_recovered("005930", Venue.KRX)
    await se.on_book(book(1))
    await se.on_vi("005930", Venue.KRX, ViState.NORMAL, NOW)
    await se.on_status("005930", Venue.KRX, halted=False, restricted=False)
    await se.on_account_sync("005930", Venue.KRX, 0, 0)
    return se


def test_ready_state_builds_compact_snapshot():
    """목적: 정상 상태에서만 스냅샷 생성, 모델 입력 400토큰 미만
    입력: 정상 호가·연결·VI NORMAL·계좌동기화
    기대: readiness=[], 스냅샷 생성 및 토큰 상한 이내"""
    se = asyncio.run(ready_engine())
    assert se.readiness("005930", Venue.KRX, NOW, REG) == []
    s = se.build_snapshot("005930", Venue.KRX, NOW, REG)
    assert s is not None and s.mid == 69950.0
    assert_within_budget(s)


def test_t07_data_feed_stop_blocks_entry():
    """목적: 데이터 수신 중단 → 신규 진입 차단
    입력: (a) 마지막 수신 후 3초 경과 (b) WebSocket 단절 후 재연결만 되고 gap 미복구
    기대: stale_data / feed_disconnected / data_gap_unrecovered 사유, 스냅샷 None"""
    se = asyncio.run(ready_engine())
    later = NOW + timedelta(seconds=3)
    assert "stale_data" in se.readiness("005930", Venue.KRX, later, REG)
    assert se.build_snapshot("005930", Venue.KRX, later, REG) is None
    asyncio.run(se.on_connection("005930", Venue.KRX, False))
    r = se.readiness("005930", Venue.KRX, NOW, REG)
    assert "feed_disconnected" in r and "data_gap_unrecovered" in r
    asyncio.run(se.on_connection("005930", Venue.KRX, True))
    assert "data_gap_unrecovered" in se.readiness("005930", Venue.KRX, NOW, REG)


def test_t08_vi_blocks_entry_and_stabilization():
    """목적: VI 발동 → 신규 진입 차단, 해제 직후 안정화 기간 적용
    입력: VI_ACTIVE → NORMAL 전환 후 60초/121초
    기대: vi_vi_active → vi_stabilization → 통과"""
    se = asyncio.run(ready_engine())
    asyncio.run(se.on_vi("005930", Venue.KRX, ViState.VI_ACTIVE, NOW))
    assert "vi_vi_active" in se.readiness("005930", Venue.KRX, NOW, REG)
    asyncio.run(se.on_vi("005930", Venue.KRX, ViState.NORMAL, NOW))
    for sec, expect in ((60, True), (121, False)):
        t = NOW + timedelta(seconds=sec)
        asyncio.run(se.on_book(book(10 + sec, t=t)))
        assert ("vi_stabilization" in se.readiness("005930", Venue.KRX, t, REG)) is expect
    asyncio.run(se.on_vi("005930", Venue.KRX, ViState.UNKNOWN, NOW))
    assert "vi_unknown" in se.readiness("005930", Venue.KRX, NOW + timedelta(seconds=121), REG)


def test_t09_halted_security_blocks_entry():
    """목적: 거래정지 종목 → 신규 진입 차단 (상태 미확인도 차단)
    입력: halted=True, 그리고 새 종목(halted=None)
    기대: halted_or_unknown"""
    se = asyncio.run(ready_engine())
    asyncio.run(se.on_status("005930", Venue.KRX, halted=True))
    assert "halted_or_unknown" in se.readiness("005930", Venue.KRX, NOW, REG)
    assert "halted_or_unknown" in se.readiness("000660", Venue.KRX, NOW, REG)


def test_stale_sequence_cannot_overwrite_latest():
    """목적: 구형 스냅샷이 최신 상태를 덮어쓰지 못함
    입력: seq 5 적용 후 seq 4 도착
    기대: seq 4 거부, 최신 유지"""
    ob = OrderBook("005930", Venue.KRX)
    assert ob.apply(book(5, bid="70000", ask="70100"))
    assert not ob.apply(book(4))
    assert ob.best_bid == Decimal("70000") and ob.rejected_stale == 1


def test_obi_not_fabricated_on_invalid_book():
    """목적: 분모 0 또는 교차 호가에서 OBI/mid 생성 금지
    입력: 잔량 0 호가, 교차 호가
    기대: None"""
    ob = OrderBook("005930", Venue.KRX)
    ob.apply(book(1, bq=0, aq=0))
    assert ob.obi() is None
    ob.apply(book(2, bid="70100", ask="70000"))
    assert ob.mid() is None and ob.obi() is None and "crossed_or_locked" in ob.validity_issues()
