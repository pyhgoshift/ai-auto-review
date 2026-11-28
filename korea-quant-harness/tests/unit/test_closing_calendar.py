"""마감 동시호가 방어·거래 캘린더(T10, T11, T21)."""
import asyncio
from datetime import date

from conftest import kst
from kqh.domain import Venue
from kqh.risk.closing_gate import ClosingGate, ClosingPhase
from kqh.state.market_clock import SessionName


def test_t10_entry_blocked_from_1515(cal, rig):
    """목적: 15:15 이후 데이트레이딩 신규 진입 차단
    입력: 15:14:59 / 15:15:00 / 15:19:59 시각의 진입 요청
    기대: 15:14:59만 허용, RiskEngine도 closing_gate 사유로 거부"""
    g = ClosingGate(cal)
    assert g.new_entry_allowed(kst(15, 14, 59))
    assert not g.new_entry_allowed(kst(15, 15, 0))
    assert g.phase_at(kst(15, 19, 59)) == ClosingPhase.ENTRY_BLOCK_LIQUIDATE
    assert g.should_liquidate_daytrade(kst(15, 15, 0))
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(), kst(15, 16)))
    assert not d.approved and "closing_gate_allows_entry" in d.blocked_by


def test_t11_closing_auction_defense_and_unliquidated_kept(cal):
    """목적: 15:20 동시호가 진입 방어, 미청산 잔고를 없어진 것으로 기록하지 않음
    입력: 15:20:00, 데이트레이딩 잔고 {005930: 37}, 오버나이트 승인 {000660}
    기대: AUCTION_DEFENSE, 신규 진입 불가, 005930 37주 EMERGENCY 알림, 000660은 알림 제외"""
    g = ClosingGate(cal)
    t = kst(15, 20, 0)
    assert g.phase_at(t) == ClosingPhase.AUCTION_DEFENSE
    assert not g.new_entry_allowed(t, is_daytrade=False)
    alerts = g.unliquidated(t, {"005930": 37, "000660": 10}, overnight_approved={"000660"})
    assert [(a.ticker, a.remaining_qty, a.severity) for a in alerts] == [("005930", 37, "EMERGENCY")]
    assert g.phase_at(kst(15, 30)) == ClosingPhase.POST_CLOSE


def test_t21_special_day_calendar(cal):
    """목적: 특별 거래일 → 변경된 캘린더 적용, 미지의 날짜는 UNKNOWN
    입력: 2026-11-19(60분 지연 예시), 공휴일 2026-12-25, 범위 밖 2027-01-04
    기대: 동시호가 16:20·방어 16:15 / NON_TRADING / UNKNOWN(진입 차단)"""
    sd = date(2026, 11, 19)
    g = ClosingGate(cal)
    assert cal.closing_auction_start(sd) == kst(16, 20, d=sd)
    assert g.phase_at(kst(15, 16, d=sd)) == ClosingPhase.NORMAL
    assert g.phase_at(kst(16, 15, d=sd)) == ClosingPhase.ENTRY_BLOCK_LIQUIDATE
    assert cal.session_at(Venue.KRX, kst(9, 30, d=sd)).session == SessionName.PRE_OPEN_AUCTION
    assert g.phase_at(kst(10, 0, d=date(2026, 12, 25))) == ClosingPhase.NON_TRADING
    unk = kst(10, 0, d=date(2027, 1, 4))
    assert g.phase_at(unk) == ClosingPhase.UNKNOWN and not g.new_entry_allowed(unk)
    assert cal.session_at(Venue.KRX, unk).session == SessionName.UNKNOWN


def test_venues_modeled_separately(cal):
    """목적: KRX와 NXT를 별도 거래장소로 모델링
    입력: 08:10 (NXT 프리마켓 시간대, KRX 정규장 전)
    기대: NXT=PRE_MARKET, KRX=CLOSED"""
    t = kst(8, 10)
    assert cal.session_at(Venue.NXT, t).session == SessionName.PRE_MARKET
    assert cal.session_at(Venue.KRX, t).session == SessionName.CLOSED
