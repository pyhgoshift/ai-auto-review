"""PAPER 파이프라인 시연(외부 연결 없음). 입력 호가는 [예시 데이터 - 실데이터 연동 필요].

SIGNAL → VALIDATION → HARD RISK CHECK → OMS → PaperBroker 의 단일 경로를 따라
각 단계의 통과/차단 근거를 출력한다. 기준선 모델은 보정되지 않았으므로(0.5)
정상적으로는 '거래하지 않음'이 결과다: NO VERIFIED EDGE, NO TRADE.

  python scripts/run_paper.py                 # 저장소 설정 그대로 (UNAPPROVED → 차단)
  python scripts/run_paper.py --demo-approve  # 메모리에서만 PAPER_APPROVED로 가정
"""
from __future__ import annotations

import argparse
import asyncio
import json
import sys
import tempfile
from datetime import datetime, timedelta
from decimal import Decimal
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from kqh.config import RiskLimits, load_risk_limits, load_yaml  # noqa: E402
from kqh.data.event_store import EventStore  # noqa: E402
from kqh.domain import KST, Market, Mode, Venue, ViState  # noqa: E402
from kqh.execution.order_manager import OrderManager  # noqa: E402
from kqh.execution.paper_broker import PaperBroker  # noqa: E402
from kqh.execution.transaction_cost import CostModel  # noqa: E402
from kqh.reflex.inference import DeterministicBaseline, ReflexRunner  # noqa: E402
from kqh.reflex.schemas import ReflexSignal  # noqa: E402
from kqh.reflex.signal_validator import EntryContext, evaluate_entry  # noqa: E402
from kqh.risk.closing_gate import ClosingGate  # noqa: E402
from kqh.risk.exposure import Portfolio  # noqa: E402
from kqh.risk.kill_switch import KillSwitch  # noqa: E402
from kqh.risk.risk_engine import EntryRequest, RiskEngine  # noqa: E402
from kqh.state.market_clock import TradingCalendar  # noqa: E402
from kqh.state.orderbook import BookUpdate, Level  # noqa: E402
from kqh.state.state_engine import StateEngine  # noqa: E402
from kqh.state.tick_size import TickRegistry, price_limit_band  # noqa: E402


async def main(demo_approve: bool) -> int:
    cfg = ROOT / "configs"
    limits = load_risk_limits(cfg / "risk_limits.yaml")
    if demo_approve and limits is not None:
        raw = load_yaml(cfg / "risk_limits.yaml")
        raw["approval"].update(status="PAPER_APPROVED", approved_by="DEMO(in-memory)", approved_at="demo")
        limits = RiskLimits.model_validate(raw)
    cal = TradingCalendar.from_file(cfg / "market_calendar.yaml")
    tick = TickRegistry(load_yaml(cfg / "market_calendar.yaml")["tick_tables"]).table_for(Market.KOSPI, datetime(2026, 11, 26).date())
    now = datetime(2026, 11, 26, 10, 0, tzinfo=KST)   # [예시 데이터] 시연 시각
    T, V = "005930", Venue.KRX

    se = StateEngine(limits.execution.max_data_staleness_ms if limits else 1, 120, 1500)
    await se.on_connection(T, V, True)
    await se.on_gap_recovered(T, V)
    bids = tuple(Level(Decimal(69900 - 100 * i), 900) for i in range(10))
    asks = tuple(Level(Decimal(70000 + 100 * i), 300) for i in range(10))
    await se.on_book(BookUpdate(T, V, 1, now, now, bids, asks))
    for i in range(60):
        await se.on_trade(T, V, 69900 + (i % 5) * 20, 10, i % 3 != 0, now)
    await se.on_vi(T, V, ViState.NORMAL, now - timedelta(minutes=10))
    await se.on_status(T, V, halted=False, restricted=False)
    await se.on_account_sync(T, V, 0, 0)

    session = cal.session_at(V, now)
    snap = se.build_snapshot(T, V, now, session)
    report: dict = {"note": "[예시 데이터 - 실데이터 연동 필요]", "readiness": se.readiness(T, V, now, session)}
    if snap is None:
        report["result"] = "NO_SNAPSHOT"
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0
    report["model_input"] = snap.to_model_input()
    runner = ReflexRunner(DeterministicBaseline(), max_concurrency=4, max_latency_ms=500)
    sig = await runner.evaluate(snap, "C-DEMO")
    report["signal"] = json.loads(sig.model_dump_json())

    cm = CostModel.from_file(str(cfg / "costs.yaml"))
    cost = cm.round_trip_bps(Market.KOSPI, snap.spread_bps, 2_000_000, 5e11)
    report["round_trip_cost_bps"] = round(cost, 2)
    # 전략 단위 실증 통계가 없으므로 순기대값은 산출 불가 → None (값을 만들어내지 않음)
    pf = Portfolio(cash=100_000_000, start_of_day_equity=100_000_000, peak_equity=100_000_000)
    kill = KillSwitch(kill_file=ROOT / "run" / "KILL")
    risk = RiskEngine(limits, Mode.PAPER, kill, pf, ClosingGate(cal))
    ctx = EntryContext(now=now, deterministic_risk_state=risk.deterministic_risk_state() if limits else "reduce",
                       calibration_valid=False, data_quality_valid=True, net_ev_bps=None,
                       kill_switch_active=kill.is_active(), session_allowed=session.allows_continuous_entry,
                       restricted_security=False, vi_entry_block=False, pending_conflicting_order=False,
                       snapshot_expires_at=snap.expires_at)
    dec = evaluate_entry(sig, ctx, limits)
    report["entry_gate"] = {"approved": dec.approved, "blocked_by": dec.blocked_by,
                            "escalate_to_system2": dec.escalate_to_system2}

    if isinstance(sig, ReflexSignal):
        req = EntryRequest(signal_id=sig.snapshot_id, strategy_id="DEMO", candidate_id="C-DEMO", ticker=T, venue=V,
                           market=Market.KOSPI, sector="SEMI", limit_price=Decimal("70000"), desired_qty=0,
                           stop_loss_bps=100, adv_value=5e11, book_ask_depth_qty=3000, spread_bps=snap.spread_bps,
                           api_latency_ms=50, entry_decision=dec, tick_table=tick,
                           price_band=price_limit_band(Decimal("70000"), Decimal("30"), tick),
                           venue_supports_order=True)
        rd = await risk.approve_entry(req, now)
        report["risk_engine"] = {"approved": rd.approved, "blocked_by": rd.blocked_by}
        if rd.approved:   # 이 시연 구성에서는 도달하지 않아야 한다
            with tempfile.TemporaryDirectory() as td:
                oms = OrderManager(PaperBroker(cash_krw=1e8), risk, EventStore(Path(td) / "e.jsonl"))
                await oms.recover()
                o = await oms.submit(rd.approval, now)
                report["order"] = o.status.value
    report["result"] = "NO TRADE" if "order" not in report else report["order"]
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--demo-approve", action="store_true")
    raise SystemExit(asyncio.run(main(ap.parse_args().demo_approve)))
