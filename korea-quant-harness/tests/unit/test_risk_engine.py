"""위험 엔진(T15–T17, T22, T23) 및 사이징."""
import asyncio
from decimal import Decimal
from pathlib import Path

import pytest

from conftest import CFG, NOW, hold, make_ctx, make_signal
from kqh.config import load_risk_limits
from kqh.domain import Market, Venue
from kqh.execution.reconciliation import reconcile_positions
from kqh.execution.transaction_cost import CostModel, net_ev_bps
from kqh.reflex.signal_validator import evaluate_entry
from kqh.risk.kill_switch import KillType
from kqh.risk.position_sizing import StrategyStats, kelly_fraction


def test_control_entry_approved_with_caps(rig):
    """목적: 정상 조건에서 승인되며 수량은 가장 작은 한도로 제한됨(양성 대조군)
    입력: desired 20주 @70,000, 주문금액 상한 300만원
    기대: approved, qty=min(20, 42)=20; desired 100 → order_value 상한 42주"""
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(), NOW))
    assert d.approved and d.approval.qty == 20
    d2 = asyncio.run(rig.risk.approve_entry(rig.entry_request(signal_id="SIG-2", desired_qty=100), NOW))
    assert d2.approved and d2.approval.qty == 42


def test_shipped_unapproved_limits_block(tmp_path, cal, tick):
    """목적: 저장소에 포함된 위험 한도(UNAPPROVED)로는 PAPER 주문도 불가
    입력: configs/risk_limits.yaml 원본
    기대: approval_status_for_mode 실패"""
    from conftest import build_rig
    r = build_rig(tmp_path, load_risk_limits(CFG / "risk_limits.yaml"), cal, tick)
    d = asyncio.run(r.risk.approve_entry(r.entry_request(), NOW))
    assert not d.approved and "approval_status_for_mode" in d.blocked_by


def test_missing_or_partial_limits_file_returns_none(tmp_path):
    """목적: 설정 누락/필드 누락 시 None(거래 금지)
    입력: 없는 파일, 필드 하나가 빠진 파일
    기대: None"""
    assert load_risk_limits(tmp_path / "nope.yaml") is None
    p = tmp_path / "partial.yaml"
    p.write_text((CFG / "risk_limits.yaml").read_text().replace("max_daily_loss_krw: 300000\n", ""))
    assert load_risk_limits(p) is None


def test_t15_daily_loss_limit_blocks_new_orders_but_not_exit(rig):
    """목적: 일일 손실 한도 도달 → 신규 주문 금지, 청산 경로는 유지
    입력: 보유 100주 평가손 -300,000원(한도)
    기대: risk_state=reduce, AUTOMATIC_KILL, 진입 거부, 청산 승인"""
    hold(rig, qty=100, price=70000.0)
    rig.portfolio.positions["005930"].market_price = 67000.0   # -3,000 x 100
    assert rig.risk.deterministic_risk_state() == "reduce"
    assert any("AUTOMATIC_KILL" in r for r in rig.kill.reasons())
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(), NOW))
    assert not d.approved and "kill_switch_inactive" in d.blocked_by
    rd = asyncio.run(rig.risk.approve_reduce(rig.reduce_request(qty=100, limit_price=Decimal("67000")), NOW))
    assert rd.approved


def test_near_limit_state(rig):
    """목적: 한도 사용률 70% 이상 → near_limit(신규 진입 불가)
    입력: 일일손실 -220,000원(73%)
    기대: near_limit"""
    hold(rig, qty=100, price=70000.0)
    rig.portfolio.positions["005930"].market_price = 67800.0
    assert rig.risk.deterministic_risk_state() == "near_limit"


def test_t16_kill_switch_blocks_and_requires_operator(rig, tmp_path):
    """목적: 킬 스위치 작동 → 신규 주문 금지, 자동 해제 불가
    입력: MANUAL_KILL / 외부 KILL 파일 / 승인 후 제출 직전 킬
    기대: 진입 거부, 파일 존재 시 해제 실패, 운영자 정보 없는 해제 예외, 제출 직전 재검증으로 REJECTED"""
    rig.kill.trigger(KillType.MANUAL_KILL, "operator", "op1")
    assert not asyncio.run(rig.risk.approve_entry(rig.entry_request(), NOW)).approved
    with pytest.raises(PermissionError):
        rig.kill.release("", "")
    assert rig.kill.release("op1", "TICKET-1")
    (tmp_path / "KILL").write_text("stop")
    assert rig.kill.is_active() and not rig.kill.release("op1", "TICKET-2")
    (tmp_path / "KILL").unlink()
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(), NOW))
    assert d.approved
    rig.kill.trigger(KillType.MANUAL_KILL, "late", "op1")
    o = asyncio.run(rig.oms.submit(d.approval, NOW))
    assert o.status.value == "REJECTED" and "kill_switch_inactive" in o.reject_reason
    assert rig.broker.submit_calls == 0 and not rig.portfolio.reserved_buy


def test_t17_spread_widening_recomputes_cost(rig, limits):
    """목적: 스프레드 급확대 → 거래비용 재계산, EV 음수 전환 시 진입 거부
    입력: 스프레드 10bps → 60bps, 총 기대이익 W=150bps, L=60bps, p=0.6
    기대: 비용 50bps 증가, EV 양수→음수, 게이트/위험엔진 모두 거부"""
    cm = CostModel.from_file(str(CFG / "costs.yaml"))
    c1 = cm.round_trip_bps(Market.KOSPI, 10, 2_000_000, 5e11)
    c2 = cm.round_trip_bps(Market.KOSPI, 60, 2_000_000, 5e11)
    assert c2 - c1 == pytest.approx(50)
    ev1, ev2 = net_ev_bps(0.6, 150, 60, c1), net_ev_bps(0.6, 150, 60, c2)
    assert ev1 > limits.signal.min_net_ev_margin_bps and ev2 < 0
    assert not evaluate_entry(make_signal(), make_ctx(net_ev_bps=ev2), limits).approved
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(spread_bps=60.0), NOW))
    assert "spread_within_limit" in d.blocked_by
    assert cm.round_trip_bps(Market.KOSPI, 10, 2_000_000, 0) == float("inf")


def test_t22_venue_order_condition_mismatch_rejected(rig, tick):
    """목적: 거래소별 주문 조건 불일치 → 주문 거부
    입력: (a) venue_supports_order=False (b) 호가단위 위반 70,050원 (c) 가격제한폭 밖
          (d) 위험엔진 통과 후 브로커가 NXT 미지원
    기대: (a)(b)(c) 위험엔진 거부, (d) REJECTED 및 예약 해제"""
    for ov, name in (({"venue_supports_order": False}, "venue_supports_order"),
                     ({"limit_price": Decimal("70050")}, "valid_tick"),
                     ({"limit_price": Decimal("95000")}, "within_price_band")):
        d = asyncio.run(rig.risk.approve_entry(rig.entry_request(**ov), NOW))
        assert name in d.blocked_by, name
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(venue=Venue.NXT), NOW))
    o = asyncio.run(rig.oms.submit(d.approval, NOW))
    assert o.status.value == "REJECTED" and o.reject_reason.startswith("venue_not_supported")
    assert not rig.portfolio.reserved_buy


def test_t23_ledger_mismatch_blocks(rig):
    """목적: 계좌 잔고와 내부 장부 불일치 → 신규 주문 차단
    입력: 내부 100주, 브로커 90주
    기대: ledger_mismatch 플래그, 진입 거부; 일치 후 해제"""
    hold(rig, qty=100)
    rig.broker.set_position("005930", 90)
    rep = asyncio.run(reconcile_positions(rig.broker, rig.risk))
    assert not rep.matched and rep.mismatches == {"005930": (100, 90)}
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(), NOW))
    assert "system_flags_clear" in d.blocked_by
    rig.broker.set_position("005930", 100)
    assert asyncio.run(reconcile_positions(rig.broker, rig.risk)).matched
    assert "ledger_mismatch" not in rig.risk.flags


def test_concurrent_approvals_cannot_exceed_exposure(tmp_path, limits_raw, cal, tick):
    """목적: 동시 주문에서도 위험 예산이 원자적으로 예약되어 한도 초과 불가
    입력: 총익스포저 한도 2,000만원, 300만원 주문 20건 동시 승인 요청
    기대: 승인 합계 ≤ 2,000만원 (42주×6건 + 잔여 33주 1건 = 7건)"""
    from conftest import build_rig
    from kqh.config import RiskLimits
    limits_raw["position"]["max_position_weight_pct"] = 100
    limits_raw["position"]["max_sector_weight_pct"] = 100
    r = build_rig(tmp_path, RiskLimits.model_validate(limits_raw), cal, tick)

    async def go():
        reqs = [r.entry_request(signal_id=f"S{i}", ticker=f"{i:06d}", desired_qty=1000) for i in range(20)]
        return await asyncio.gather(*(r.risk.approve_entry(q, NOW) for q in reqs))
    res = asyncio.run(go())
    total = sum(float(d.approval.limit_price) * d.approval.qty for d in res if d.approved)
    assert total <= r.risk.limits.account.max_gross_exposure_krw
    assert sum(d.approved for d in res) == 7


def test_kelly_zero_conditions_and_cap():
    """목적: Kelly 0 처리 조건과 Quarter Kelly 상한
    입력: 보정 실패/표본 부족/손실꼬리/EV≤0, 정상 통계(p=0.55,b=1.5,n=1000)
    기대: 비정상은 0, 정상은 0 < f ≤ 0.25·f_full(하한 승률 사용)"""
    base = dict(strategy_id="s", n_trades=1000, win_rate=0.55, avg_net_win=1.5, avg_net_loss=1.0,
                calibration_valid=True, heavy_left_tail=False, data_quality_valid=True)
    for ov in ({"calibration_valid": False}, {"n_trades": 50}, {"heavy_left_tail": True},
               {"win_rate": 0.3}, {"avg_net_loss": 0.0}):
        assert kelly_fraction(StrategyStats(**{**base, **ov}), cap=0.25, min_trades=200,
                              risk_blocked=False).fraction == 0.0, ov
    k = kelly_fraction(StrategyStats(**base), cap=0.25, min_trades=200, risk_blocked=False)
    naive_full = 0.55 - 0.45 / 1.5
    assert 0 < k.fraction <= 0.25 * naive_full and k.f_full < naive_full
    assert kelly_fraction(StrategyStats(**base), cap=0.25, min_trades=200, risk_blocked=True).fraction == 0.0


def test_live_requires_live_approval_and_verified_reference_data(tmp_path, limits_raw, cal, tick):
    """목적: LIVE 모드는 LIVE_APPROVED 한도 + 검증된 기준데이터(캘린더·호가단위·세율)가 모두 필요
    입력: PAPER_APPROVED 한도로 LIVE / LIVE_APPROVED지만 reference_data_verified=False
    기대: 둘 다 거부"""
    from conftest import build_rig
    from kqh.config import RiskLimits
    from kqh.domain import Mode
    r = build_rig(tmp_path, RiskLimits.model_validate(limits_raw), cal, tick)
    r.risk.mode = Mode.LIVE
    d = asyncio.run(r.risk.approve_entry(r.entry_request(), NOW))
    assert {"approval_status_for_mode", "reference_data_verified"} <= set(d.blocked_by)
    limits_raw["approval"]["status"] = "LIVE_APPROVED"
    r.risk.limits = RiskLimits.model_validate(limits_raw)
    d = asyncio.run(r.risk.approve_entry(r.entry_request(), NOW))
    assert d.blocked_by == ["reference_data_verified"]
