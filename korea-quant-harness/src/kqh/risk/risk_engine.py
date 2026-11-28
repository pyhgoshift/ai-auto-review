"""결정론적 위험 엔진 — 모델이 모든 조건을 '매수'로 내도 독립적으로 거부할 수 있는 유일한 승인 주체.

- 위험 예산은 asyncio.Lock 하에서 원자적으로 '예약'된다(동시 주문 한도 초과 방지).
- 승인서(RiskApproval)는 1회용·만료형이며, OMS가 제출 직전 redeem()으로 재검증한다.
- ENTRY(위험 증가)와 REDUCE(청산) 경로를 분리한다. 킬스위치·데이터장애는 ENTRY만 막는다.
"""
from __future__ import annotations

import asyncio
import logging
import math
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from decimal import Decimal

from ..config import RiskLimits
from ..reflex.schemas import ConditionResult, EntryDecision
from ..state.tick_size import TickTable, is_valid_tick
from ..domain import IntentKind, Market, Mode, Side, Venue
from .closing_gate import ClosingGate
from .exposure import Portfolio
from .kill_switch import KillSwitch, KillType

log = logging.getLogger(__name__)

RISK_STATES = ("safe", "near_limit", "reduce")


@dataclass(frozen=True)
class EntryRequest:
    signal_id: str
    strategy_id: str
    candidate_id: str
    ticker: str
    venue: Venue
    market: Market
    sector: str
    limit_price: Decimal
    desired_qty: int                 # 포지션 사이징(Kelly 등) 결과
    stop_loss_bps: float             # 최대 손실 산정용. 0 이하면 거부
    adv_value: float                 # 일평균 거래대금(KRW)
    book_ask_depth_qty: int          # 최우선~3호가 매도잔량
    spread_bps: float | None
    api_latency_ms: float | None
    entry_decision: EntryDecision
    tick_table: TickTable
    price_band: tuple[Decimal, Decimal] | None
    venue_supports_order: bool       # 거래장소·호가유형·종목 지원 여부(검증된 값)
    is_daytrade: bool = True


@dataclass(frozen=True)
class ReduceRequest:
    ticker: str
    venue: Venue
    qty: int
    limit_price: Decimal
    tick_table: TickTable
    price_band: tuple[Decimal, Decimal] | None
    tradable: bool                   # 거래정지/VI 등으로 청산 불가하면 False
    venue_supports_order: bool
    reason: str


@dataclass(frozen=True)
class RiskApproval:
    approval_id: str
    kind: IntentKind
    signal_id: str | None
    strategy_id: str | None
    candidate_id: str | None
    ticker: str
    venue: Venue
    side: Side
    qty: int
    limit_price: Decimal
    issued_at: datetime
    expires_at: datetime
    checks: tuple[ConditionResult, ...]


@dataclass(frozen=True)
class RiskDecision:
    approved: bool
    approval: RiskApproval | None
    checks: tuple[ConditionResult, ...]

    @property
    def blocked_by(self) -> list[str]:
        return [c.name for c in self.checks if not c.passed]


@dataclass
class RiskEngine:
    limits: RiskLimits | None
    mode: Mode
    kill: KillSwitch
    portfolio: Portfolio
    closing_gate: ClosingGate
    approval_ttl: timedelta = timedelta(seconds=2)
    reference_data_verified: bool = False   # 캘린더·호가단위·세율이 공식 원문으로 검증되었는가(LIVE 필수)
    flags: dict[str, str] = field(default_factory=dict)       # 시스템 위험 플래그(이름→사유)
    _issued: dict[str, RiskApproval] = field(default_factory=dict)
    _redeemed: set[str] = field(default_factory=set)
    _lock: asyncio.Lock = field(default_factory=asyncio.Lock)

    # ---------- 상태 ----------
    def set_flag(self, name: str, reason: str) -> None:
        self.flags[name] = reason
        log.error("risk_flag_set", extra={"flag": name, "reason": reason})

    def clear_flag(self, name: str) -> None:
        self.flags.pop(name, None)

    def utilization_pct(self) -> float:
        if self.limits is None:
            return math.inf
        a, p = self.limits.account, self.portfolio
        loss = max(0.0, -p.daily_pnl)
        dd_day = loss / p.start_of_day_equity * 100 if p.start_of_day_equity > 0 else math.inf
        cum = max(0.0, (p.peak_equity - p.equity) / p.peak_equity * 100) if p.peak_equity > 0 else math.inf
        return max(loss / a.max_daily_loss_krw, dd_day / a.max_daily_drawdown_pct,
                   cum / a.max_cumulative_drawdown_pct) * 100

    def deterministic_risk_state(self) -> str:
        u = self.utilization_pct()
        if u >= 100:
            if not any(r.startswith("AUTOMATIC_KILL:loss_limit") for r in self.kill.reasons()):
                self.kill.trigger(KillType.AUTOMATIC_KILL, "loss_limit_reached")
            return "reduce"
        if u >= self.limits.account.near_limit_utilization_pct:
            return "near_limit"
        return "safe"

    def _mode_approved(self) -> bool:
        if self.limits is None:
            return False
        st = self.limits.approval.status
        return st == "LIVE_APPROVED" if self.mode == Mode.LIVE else st in ("PAPER_APPROVED", "LIVE_APPROVED")

    def _global_entry_checks(self, now: datetime, is_daytrade: bool) -> list[ConditionResult]:
        c: list[ConditionResult] = []

        def add(n: str, ok: bool, obs: object, req: str) -> None:
            c.append(ConditionResult(name=n, passed=bool(ok), observed=str(obs), required=req))

        add("risk_limits_loaded", self.limits is not None, self.limits is not None, "True")
        add("approval_status_for_mode", self._mode_approved(),
            None if self.limits is None else self.limits.approval.status, f"approved for {self.mode}")
        if self.mode == Mode.LIVE:
            add("reference_data_verified", self.reference_data_verified, self.reference_data_verified, "True")
        add("kill_switch_inactive", not self.kill.is_active(), self.kill.reasons(), "[]")
        add("system_flags_clear", not self.flags, sorted(self.flags), "[]")
        state = self.deterministic_risk_state() if self.limits else "reduce"
        add("deterministic_risk_state", state == "safe", state, "safe")
        add("closing_gate_allows_entry", self.closing_gate.new_entry_allowed(now, is_daytrade),
            self.closing_gate.phase_at(now).value, "NORMAL")
        return c

    # ---------- ENTRY ----------
    async def approve_entry(self, r: EntryRequest, now: datetime) -> RiskDecision:
        async with self._lock:
            c = self._global_entry_checks(now, r.is_daytrade)

            def add(n: str, ok: bool, obs: object, req: str) -> None:
                c.append(ConditionResult(name=n, passed=bool(ok), observed=str(obs), required=req))

            add("entry_decision_approved", r.entry_decision.approved, r.entry_decision.blocked_by, "[]")
            add("venue_supports_order", r.venue_supports_order, r.venue_supports_order, "True")
            add("valid_tick", r.limit_price > 0 and is_valid_tick(r.limit_price, r.tick_table), r.limit_price, "on tick grid")
            in_band = r.price_band is not None and r.price_band[0] <= r.limit_price <= r.price_band[1]
            add("within_price_band", in_band, r.price_band, "lower<=price<=upper")
            if self.limits is not None:
                ex = self.limits.execution
                add("spread_within_limit", r.spread_bps is not None and r.spread_bps <= ex.max_spread_bps,
                    r.spread_bps, f"<={ex.max_spread_bps}")
                add("api_latency_ok", r.api_latency_ms is not None and r.api_latency_ms <= ex.max_api_latency_ms,
                    r.api_latency_ms, f"<={ex.max_api_latency_ms}")
            add("stop_loss_defined", r.stop_loss_bps > 0, r.stop_loss_bps, ">0")
            add("adv_known", r.adv_value > 0, r.adv_value, ">0")
            if not all(x.passed for x in c):
                return RiskDecision(False, None, tuple(c))

            qty, caps = self._qty_caps(r)
            add("qty_after_caps", qty >= 1, caps, ">=1")
            if qty < 1:
                return RiskDecision(False, None, tuple(c))
            appr = RiskApproval(str(uuid.uuid4()), IntentKind.ENTRY, r.signal_id, r.strategy_id, r.candidate_id,
                                r.ticker, r.venue, Side.BUY, qty, r.limit_price, now, now + self.approval_ttl, tuple(c))
            self.portfolio.reserved_buy[appr.approval_id] = (r.ticker, r.sector, float(r.limit_price) * qty)
            self._issued[appr.approval_id] = appr
            return RiskDecision(True, appr, tuple(c))

    def _qty_caps(self, r: EntryRequest) -> tuple[int, dict[str, int]]:
        L, p = self.limits, self.portfolio
        px = float(r.limit_price)
        eq = p.equity
        remaining_loss = L.account.max_daily_loss_krw + min(0.0, p.daily_pnl)
        caps = {
            "desired": r.desired_qty,
            "position_weight": (L.position.max_position_weight_pct / 100 * eq - p.position_value(r.ticker)) / px,
            "sector_weight": (L.position.max_sector_weight_pct / 100 * eq - p.sector_exposure(r.sector)) / px,
            "gross_exposure": (L.account.max_gross_exposure_krw - p.gross_exposure) / px,
            "order_value": L.position.max_order_value_krw / px,
            "adv_participation": L.position.max_adv_participation_pct / 100 * r.adv_value / px,
            "book_participation": L.position.max_book_participation_pct / 100 * r.book_ask_depth_qty,
            "cash": p.available_cash() / px,
            "loss_budget": remaining_loss / (px * r.stop_loss_bps / 10000),
        }
        ints = {k: max(0, math.floor(v)) for k, v in caps.items()}
        return min(ints.values()), ints

    # ---------- REDUCE (청산 안전 경로) ----------
    async def approve_reduce(self, r: ReduceRequest, now: datetime) -> RiskDecision:
        async with self._lock:
            c: list[ConditionResult] = []

            def add(n: str, ok: bool, obs: object, req: str) -> None:
                c.append(ConditionResult(name=n, passed=bool(ok), observed=str(obs), required=req))

            sellable = self.portfolio.sellable_qty(r.ticker)
            add("qty_positive", r.qty >= 1, r.qty, ">=1")
            add("reduce_only", r.qty <= sellable, f"{r.qty}/{sellable}", "qty<=sellable")
            add("tradable", r.tradable, r.tradable, "True")
            add("venue_supports_order", r.venue_supports_order, r.venue_supports_order, "True")
            add("valid_tick", r.limit_price > 0 and is_valid_tick(r.limit_price, r.tick_table), r.limit_price, "on tick grid")
            in_band = r.price_band is not None and r.price_band[0] <= r.limit_price <= r.price_band[1]
            add("within_price_band", in_band, r.price_band, "lower<=price<=upper")
            if not all(x.passed for x in c):
                if not r.tradable:
                    self.set_flag(f"unliquidatable:{r.ticker}", r.reason)   # 비상 위험 기록
                return RiskDecision(False, None, tuple(c))
            appr = RiskApproval(str(uuid.uuid4()), IntentKind.REDUCE, None, None, None, r.ticker, r.venue,
                                Side.SELL, r.qty, r.limit_price, now, now + self.approval_ttl, tuple(c))
            self.portfolio.reserved_sell_qty[appr.approval_id] = (r.ticker, r.qty)
            self._issued[appr.approval_id] = appr
            return RiskDecision(True, appr, tuple(c))

    # ---------- 제출 직전 재검증 ----------
    async def redeem(self, approval_id: str, now: datetime) -> tuple[RiskApproval | None, list[str]]:
        async with self._lock:
            a = self._issued.get(approval_id)
            if a is None:
                return None, ["unknown_approval"]
            if approval_id in self._redeemed:
                return None, ["approval_already_used"]
            if now >= a.expires_at:
                self._release_locked(approval_id)
                return None, ["approval_expired"]
            if a.kind == IntentKind.ENTRY:
                fails = [x.name for x in self._global_entry_checks(now, True) if not x.passed]
                if fails:
                    self._release_locked(approval_id)
                    return None, fails
            else:
                if a.qty > self.portfolio.sellable_qty(a.ticker) + a.qty:   # 자기 예약분 제외
                    self._release_locked(approval_id)
                    return None, ["reduce_qty_exceeds_position"]
            self._redeemed.add(approval_id)
            return a, []

    def _release_locked(self, approval_id: str) -> None:
        self.portfolio.reserved_buy.pop(approval_id, None)
        self.portfolio.reserved_sell_qty.pop(approval_id, None)

    async def release(self, approval_id: str) -> None:
        async with self._lock:
            self._release_locked(approval_id)

    async def apply_fill(self, approval_id: str, side: Side, ticker: str, qty: int, price: float,
                         sector: str = "UNKNOWN", daytrade: bool = True) -> None:
        """체결 반영. 예약분은 체결 수량만큼 줄인다. (수수료·세금은 장부가 아닌 대사로 확정)"""
        from .exposure import Position
        async with self._lock:
            p = self.portfolio
            if side == Side.BUY:
                if approval_id in p.reserved_buy:
                    sector = p.reserved_buy[approval_id][1]
                p.cash -= qty * price
                pos = p.positions.get(ticker)
                if pos is None:
                    p.positions[ticker] = Position(ticker, qty, price, price, sector, daytrade)
                else:
                    pos.avg_price = (pos.avg_price * pos.qty + price * qty) / (pos.qty + qty)
                    pos.qty += qty
                if approval_id in p.reserved_buy:
                    t, s, v = p.reserved_buy[approval_id]
                    a = self._issued[approval_id]
                    nv = v - float(a.limit_price) * qty
                    if nv <= 0:
                        p.reserved_buy.pop(approval_id)
                    else:
                        p.reserved_buy[approval_id] = (t, s, nv)
            else:
                pos = p.positions[ticker]
                p.cash += qty * price
                p.realized_pnl_today += (price - pos.avg_price) * qty
                pos.qty -= qty
                if pos.qty == 0:
                    del p.positions[ticker]
                if approval_id in p.reserved_sell_qty:
                    t, q = p.reserved_sell_qty[approval_id]
                    if q - qty <= 0:
                        p.reserved_sell_qty.pop(approval_id)
                    else:
                        p.reserved_sell_qty[approval_id] = (t, q - qty)
            p.peak_equity = max(p.peak_equity, p.equity)
