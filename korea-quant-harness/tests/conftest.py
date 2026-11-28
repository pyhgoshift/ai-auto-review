"""공용 픽스처. 모든 시장 수치는 [예시 데이터 - 실데이터 연동 필요]이며 로직 검증용이다."""
from __future__ import annotations

import copy
from dataclasses import dataclass
from datetime import date, datetime, timedelta
from decimal import Decimal
from pathlib import Path

import pytest

from kqh.config import RiskLimits, load_yaml
from kqh.data.event_store import EventStore
from kqh.domain import KST, Market, Mode, Venue
from kqh.execution.order_manager import OrderManager
from kqh.execution.paper_broker import PaperBroker
from kqh.reflex.schemas import ConditionResult, EntryDecision, ReflexSignal
from kqh.reflex.signal_validator import EntryContext
from kqh.risk.closing_gate import ClosingGate
from kqh.risk.exposure import Portfolio, Position
from kqh.risk.kill_switch import KillSwitch
from kqh.risk.risk_engine import EntryRequest, ReduceRequest, RiskEngine
from kqh.state.market_clock import TradingCalendar
from kqh.state.tick_size import TickRegistry

ROOT = Path(__file__).resolve().parents[1]
CFG = ROOT / "configs"
TRADE_DAY = date(2026, 11, 26)          # 목요일, 캘린더 범위 내 정상 거래일
assert TRADE_DAY.weekday() == 3


def kst(h: int, m: int, s: int = 0, d: date = TRADE_DAY) -> datetime:
    return datetime(d.year, d.month, d.day, h, m, s, tzinfo=KST)


NOW = kst(10, 0)


@pytest.fixture
def limits_raw() -> dict:
    raw = load_yaml(CFG / "risk_limits.yaml")
    raw = copy.deepcopy(raw)
    raw["approval"]["status"] = "PAPER_APPROVED"
    raw["approval"]["approved_by"] = "test-operator"
    raw["approval"]["approved_at"] = "2026-11-26T08:00:00+09:00"
    return raw


@pytest.fixture
def limits(limits_raw) -> RiskLimits:
    return RiskLimits.model_validate(limits_raw)


@pytest.fixture
def cal() -> TradingCalendar:
    return TradingCalendar.from_file(CFG / "market_calendar.yaml")


@pytest.fixture
def tick():
    raw = load_yaml(CFG / "market_calendar.yaml")
    return TickRegistry(raw["tick_tables"]).table_for(Market.KOSPI, TRADE_DAY)


def make_signal(**ov) -> ReflexSignal:
    base = dict(regime="trending", direction="long", toxic_flow=False, setup_quality=3, calibrated_prob=0.85,
                risk_state="safe", candidate_id="C1", ticker="005930", snapshot_id="S1",
                snapshot_timestamp=NOW, model_version="m1", signal_version="s1",
                prediction_horizon_sec=120, signal_valid_until=NOW + timedelta(seconds=5), reason_code="TEST")
    base.update(ov)
    return ReflexSignal.model_validate(base)


def make_ctx(**ov) -> EntryContext:
    base = dict(now=NOW, deterministic_risk_state="safe", calibration_valid=True, data_quality_valid=True,
                net_ev_bps=25.0, kill_switch_active=False, session_allowed=True, restricted_security=False,
                vi_entry_block=False, pending_conflicting_order=False)
    base.update(ov)
    return EntryContext(**base)


APPROVED_DECISION = EntryDecision(signal_id="S1", approved=True, escalate_to_system2=False,
                                  conditions=(ConditionResult(name="all", passed=True, observed="-", required="-"),))


@dataclass
class Rig:
    broker: PaperBroker
    risk: RiskEngine
    oms: OrderManager
    portfolio: Portfolio
    kill: KillSwitch
    store: EventStore
    tick: object
    cal: TradingCalendar

    def entry_request(self, **ov) -> EntryRequest:
        base = dict(signal_id="SIG-1", strategy_id="STRAT", candidate_id="C1", ticker="005930", venue=Venue.KRX,
                    market=Market.KOSPI, sector="SEMI", limit_price=Decimal("70000"), desired_qty=20,
                    stop_loss_bps=100.0, adv_value=5e11, book_ask_depth_qty=10000, spread_bps=10.0,
                    api_latency_ms=100.0, entry_decision=APPROVED_DECISION, tick_table=self.tick,
                    price_band=(Decimal("49000"), Decimal("91000")), venue_supports_order=True)
        base.update(ov)
        return EntryRequest(**base)

    def reduce_request(self, **ov) -> ReduceRequest:
        base = dict(ticker="005930", venue=Venue.KRX, qty=10, limit_price=Decimal("70000"), tick_table=self.tick,
                    price_band=(Decimal("49000"), Decimal("91000")), tradable=True, venue_supports_order=True,
                    reason="test")
        base.update(ov)
        return ReduceRequest(**base)


def build_rig(tmp_path: Path, limits, cal, tick, *, cash: float = 100_000_000, recovered: bool = True,
              store: EventStore | None = None, broker: PaperBroker | None = None) -> Rig:
    pf = Portfolio(cash=cash, start_of_day_equity=cash, peak_equity=cash)
    kill = KillSwitch(kill_file=tmp_path / "KILL")
    risk = RiskEngine(limits, Mode.PAPER, kill, pf, ClosingGate(cal))
    store = store or EventStore(tmp_path / "events.jsonl")
    broker = broker or PaperBroker(cash_krw=cash, clock=lambda: NOW)
    oms = OrderManager(broker, risk, store)
    oms.recovered = recovered
    return Rig(broker, risk, oms, pf, kill, store, tick, cal)


@pytest.fixture
def rig(tmp_path, limits, cal, tick) -> Rig:
    return build_rig(tmp_path, limits, cal, tick)


def hold(rig: Rig, ticker: str = "005930", qty: int = 100, price: float = 70000.0) -> None:
    """내부 장부와 모의 브로커에 동일한 보유를 설정."""
    rig.portfolio.positions[ticker] = Position(ticker, qty, price, price, "SEMI")
    rig.portfolio.cash -= qty * price
    rig.portfolio.start_of_day_equity = rig.portfolio.equity
    rig.portfolio.peak_equity = rig.portfolio.equity
    rig.broker.set_position(ticker, qty)
