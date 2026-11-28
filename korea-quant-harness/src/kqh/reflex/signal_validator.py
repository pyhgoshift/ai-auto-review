"""결정론적 신규 매수 게이트. 모든 조건을 통과해야만 RiskEngine 심사로 넘어간다.

0.80 임계값은 수익성을 보장하지 않는다(별도 검증 세트로 입증 필요).
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime

from ..config import RiskLimits
from .schemas import ConditionResult, EntryDecision, ReflexSignal, SafeNeutral


@dataclass(frozen=True)
class EntryContext:
    now: datetime
    deterministic_risk_state: str        # RiskEngine 계산값
    calibration_valid: bool
    data_quality_valid: bool
    net_ev_bps: float | None             # 비용 차감, 보수적 하한
    kill_switch_active: bool
    session_allowed: bool
    restricted_security: bool
    vi_entry_block: bool
    pending_conflicting_order: bool
    snapshot_expires_at: datetime | None = None


def evaluate_entry(sig: ReflexSignal | SafeNeutral, ctx: EntryContext, limits: RiskLimits | None) -> EntryDecision:
    if isinstance(sig, SafeNeutral) or limits is None:
        reason = "invalid_model_output" if isinstance(sig, SafeNeutral) else "risk_limits_missing"
        return EntryDecision(signal_id=getattr(sig, "snapshot_id", "invalid"), approved=False,
                             escalate_to_system2=True,
                             conditions=(ConditionResult(name=reason, passed=False, observed="-", required="valid"),))
    L = limits.signal
    c: list[ConditionResult] = []

    def add(name: str, ok: bool, obs: object, req: str) -> None:
        c.append(ConditionResult(name=name, passed=bool(ok), observed=str(obs), required=req))

    ev = ctx.net_ev_bps
    add("setup_quality", sig.setup_quality >= L.min_setup_quality, sig.setup_quality, f">={L.min_setup_quality}")
    add("calibrated_prob", sig.calibrated_prob > L.entry_prob_threshold, sig.calibrated_prob, f">{L.entry_prob_threshold}")
    add("toxic_flow", sig.toxic_flow is False, sig.toxic_flow, "False")
    add("direction", sig.direction == "long", sig.direction, "long")
    add("model_risk_state", sig.risk_state == "safe", sig.risk_state, "safe")
    add("deterministic_risk_state", ctx.deterministic_risk_state == "safe", ctx.deterministic_risk_state, "safe")
    add("calibration_valid", ctx.calibration_valid, ctx.calibration_valid, "True")
    add("data_quality_valid", ctx.data_quality_valid, ctx.data_quality_valid, "True")
    add("net_expected_value", ev is not None and ev > L.min_net_ev_margin_bps, ev, f">{L.min_net_ev_margin_bps}bps")
    add("no_active_kill_switch", not ctx.kill_switch_active, ctx.kill_switch_active, "False")
    add("allowed_trading_session", ctx.session_allowed, ctx.session_allowed, "True")
    add("no_restricted_security", not ctx.restricted_security, ctx.restricted_security, "False")
    add("no_active_vi_entry_block", not ctx.vi_entry_block, ctx.vi_entry_block, "False")
    add("no_pending_conflicting_order", not ctx.pending_conflicting_order, ctx.pending_conflicting_order, "False")
    add("signal_not_expired", ctx.now < sig.signal_valid_until, ctx.now.isoformat(), f"<{sig.signal_valid_until.isoformat()}")
    if ctx.snapshot_expires_at is not None:
        add("snapshot_not_expired", ctx.now < ctx.snapshot_expires_at, ctx.now.isoformat(), "<expires_at")

    escalate = sig.calibrated_prob < L.escalation_prob_threshold or sig.regime == "crisis"
    if sig.regime == "crisis":
        add("regime_not_crisis", False, sig.regime, "!=crisis")
    return EntryDecision(signal_id=sig.snapshot_id, approved=all(x.passed for x in c),
                         escalate_to_system2=escalate, conditions=tuple(c))
