"""OMS 장애 주입(T12–T14) 및 주문 경로 강제."""
import asyncio
from decimal import Decimal

import pytest

from conftest import NOW, build_rig, hold
from kqh.errors import OrderPathViolation
from kqh.execution.order_manager import OrderStatus as S
from kqh.operations.recovery import startup_recovery


def approve(rig, **ov):
    d = asyncio.run(rig.risk.approve_entry(rig.entry_request(**ov), NOW))
    assert d.approved, d.blocked_by
    return d.approval


def test_t12_partial_liquidation_keeps_remaining(rig):
    """목적: 청산 주문 일부만 체결 → 잔여 수량 유지
    입력: 보유 100주, 매도 100주 중 40주 체결 후 취소
    기대: PARTIALLY_FILLED → CANCELLED, 내부/브로커 보유 60주, 매도가능 60주"""
    hold(rig, qty=100)
    d = asyncio.run(rig.risk.approve_reduce(rig.reduce_request(qty=100), NOW))
    o = asyncio.run(rig.oms.submit(d.approval, NOW))
    assert o.status == S.ACKNOWLEDGED
    rig.broker.fill(o.broker_order_id, 40, 70000.0)
    asyncio.run(rig.oms.sync())
    assert o.status == S.PARTIALLY_FILLED and o.remaining == 60
    assert rig.portfolio.positions["005930"].qty == 60 and rig.portfolio.sellable_qty("005930") == 0
    asyncio.run(rig.oms.cancel(o.order_intent_id))
    assert o.status == S.CANCELLED
    assert rig.portfolio.positions["005930"].qty == 60 and rig.portfolio.sellable_qty("005930") == 60
    assert asyncio.run(rig.broker.positions()) == {"005930": 60}


def test_t13_timeout_never_resubmits(rig):
    """목적: 주문 API 타임아웃 → 중복 제출 금지, 대사로 해결
    입력: 브로커가 주문을 접수한 뒤 응답 유실(timeout_after_accept)
    기대: UNKNOWN, 같은 종목 신규 주문 거부, submit_calls=1, sync 후 브로커 주문과 연결·플래그 해제"""
    rig.broker.timeout_after_accept_next = True
    o = asyncio.run(rig.oms.submit(approve(rig), NOW))
    assert o.status == S.UNKNOWN and o.broker_order_id is None
    assert "order_state_unknown" in rig.risk.flags
    with pytest.raises(OrderPathViolation):
        asyncio.run(rig.oms.submit(_fake_retry(rig), NOW))
    assert not asyncio.run(rig.risk.approve_entry(rig.entry_request(signal_id="SIG-3", ticker="000660"), NOW)).approved
    assert rig.broker.submit_calls == 1
    asyncio.run(rig.oms.sync())
    assert o.status == S.ACKNOWLEDGED and o.broker_order_id is not None
    assert "order_state_unknown" not in rig.risk.flags
    assert len(asyncio.run(rig.broker.list_orders())) == 1


def _fake_retry(rig):
    """UNKNOWN 상태에서 같은 신호를 재전송하려는 시도(이미 사용된 신호 + 미해결 주문)."""
    from dataclasses import replace
    any_appr = next(iter(rig.risk._issued.values()))
    return replace(any_appr, approval_id="retry")


def test_t13b_timeout_before_accept_resolved_as_not_found(rig):
    """목적: 브로커가 실제로 접수하지 않은 타임아웃은 2회 미발견 확인 후 REJECTED
    입력: timeout_before_accept
    기대: UNKNOWN → RECONCILING → REJECTED, 예약 해제"""
    rig.broker.timeout_before_accept_next = True
    o = asyncio.run(rig.oms.submit(approve(rig), NOW))
    assert o.status == S.UNKNOWN
    asyncio.run(rig.oms.sync())
    assert o.status == S.RECONCILING
    asyncio.run(rig.oms.sync())
    assert o.status == S.REJECTED and not rig.portfolio.reserved_buy
    assert "order_state_unknown" not in rig.risk.flags


def test_t14_restart_reconciles_before_new_orders(tmp_path, limits, cal, tick):
    """목적: 프로세스 재시작 → 미체결 주문 대사 전 신규 주문 금지
    입력: 주문 A 접수(ACK) 후 프로세스 종료, 주문 B는 의도 기록 직후 중단(브로커 미접수)
    기대: 재시작 OMS는 recover 전 submit 거부; recover 후 A=ACKNOWLEDGED·B=REJECTED, 신규 허용"""
    r1 = build_rig(tmp_path, limits, cal, tick)
    a = asyncio.run(r1.oms.submit(approve(r1), NOW))
    assert a.status == S.ACKNOWLEDGED
    b_appr = approve(r1, signal_id="SIG-B", ticker="000660")
    r1.store.append("ORDER_INTENT", {
        "order_intent_id": "B-crash", "approval_id": b_appr.approval_id, "kind": "ENTRY", "strategy_id": "STRAT",
        "candidate_id": "C1", "signal_id": "SIG-B", "client_order_key": "KB", "ticker": "000660", "venue": "KRX",
        "side": "BUY", "qty": b_appr.qty, "limit_price": str(b_appr.limit_price),
        "order_timestamp": NOW.isoformat(), "risk_checks": []})

    r2 = build_rig(tmp_path, limits, cal, tick, recovered=False, store=r1.store, broker=r1.broker)
    with pytest.raises(OrderPathViolation):
        asyncio.run(r2.oms.submit(approve(r2, signal_id="SIG-NEW", ticker="035420"), NOW))
    rep = asyncio.run(startup_recovery(r2.oms))
    assert rep["orders_reconciled"] and rep["new_orders_allowed"]
    assert r2.oms.orders[a.order_intent_id].status == S.ACKNOWLEDGED
    assert r2.oms.orders["B-crash"].status == S.REJECTED
    assert r1.store.verify_chain()
    o = asyncio.run(r2.oms.submit(approve(r2, signal_id="SIG-NEW2", ticker="035420"), NOW))
    assert o.status == S.ACKNOWLEDGED


def test_duplicate_signal_and_forged_approval_rejected(rig):
    """목적: 동일 신호 중복 주문 방지, 위험엔진이 발급하지 않은 승인서 거부
    입력: 같은 signal_id 두 번 / 위조 approval_id / dict 객체
    기대: OrderPathViolation 또는 REJECTED(unknown_approval)"""
    asyncio.run(rig.oms.submit(approve(rig), NOW))
    from dataclasses import replace
    forged = replace(approve(rig, signal_id="SIG-X", ticker="000660"), approval_id="forged")
    o = asyncio.run(rig.oms.submit(forged, NOW))
    assert o.status == S.REJECTED and "unknown_approval" in o.reject_reason
    with pytest.raises(OrderPathViolation):
        asyncio.run(rig.oms.submit(approve(rig, signal_id="SIG-1", ticker="035720"), NOW))
    with pytest.raises(OrderPathViolation):
        asyncio.run(rig.oms.submit({"ticker": "005930", "qty": 1}, NOW))


def test_storage_failure_prevents_submit(rig, monkeypatch):
    """목적: 주문 의도 기록 실패 시 제출하지 않음
    입력: EventStore.append가 OSError
    기대: 예외 전파, submit_calls=0, storage_failure 플래그"""
    appr = approve(rig)

    def boom(*a, **k):
        raise OSError("disk full")
    monkeypatch.setattr(rig.store, "append", boom)
    with pytest.raises(OSError):
        asyncio.run(rig.oms.submit(appr, NOW))
    assert rig.broker.submit_calls == 0 and "storage_failure" in rig.risk.flags


def test_reduce_cannot_exceed_position(rig):
    """목적: 청산 경로는 보유 수량을 넘을 수 없음(공매도·포지션 반전 방지)
    입력: 보유 10주, 매도 11주
    기대: reduce_only 실패"""
    hold(rig, qty=10)
    d = asyncio.run(rig.risk.approve_reduce(rig.reduce_request(qty=11), NOW))
    assert not d.approved and "reduce_only" in d.blocked_by


def test_unliquidatable_recorded_as_emergency(rig):
    """목적: 청산 불가능 상태(거래정지 등)는 비상 위험으로 기록
    입력: tradable=False
    기대: 거부 + unliquidatable 플래그"""
    hold(rig, qty=10)
    d = asyncio.run(rig.risk.approve_reduce(rig.reduce_request(tradable=False, reason="halted"), NOW))
    assert not d.approved and "unliquidatable:005930" in rig.risk.flags
