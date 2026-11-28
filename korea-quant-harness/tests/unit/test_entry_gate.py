"""신규 매수 게이트(T01–T06, T18, T24) + 양성 대조군."""
import json

from conftest import NOW, make_ctx, make_signal
from kqh.reflex.schemas import SafeNeutral, parse_reflex_output
from kqh.reflex.signal_validator import evaluate_entry


def test_control_all_conditions_pass(limits):
    """목적: 게이트가 무조건 차단만 하는 것이 아님을 확인(양성 대조군)
    입력: 모든 조건 충족 신호/컨텍스트
    기대: approved=True"""
    d = evaluate_entry(make_signal(), make_ctx(), limits)
    assert d.approved, d.blocked_by


def test_t01_prob_079_blocked(limits):
    """목적: calibrated_prob 0.79 → 매수 금지
    입력: calibrated_prob=0.79
    기대: approved=False, blocked_by에 calibrated_prob"""
    d = evaluate_entry(make_signal(calibrated_prob=0.79), make_ctx(), limits)
    assert not d.approved and "calibrated_prob" in d.blocked_by


def test_t02_prob_080_blocked_strict_inequality(limits):
    """목적: 임계값 경계(0.80)는 엄격 초과 조건이므로 매수 금지
    입력: calibrated_prob=0.80
    기대: approved=False"""
    d = evaluate_entry(make_signal(calibrated_prob=0.80), make_ctx(), limits)
    assert not d.approved and d.blocked_by == ["calibrated_prob"]


def test_t03_setup_quality_1_blocked(limits):
    """목적: setup_quality=1 → 매수 금지
    입력: setup_quality=1
    기대: approved=False"""
    d = evaluate_entry(make_signal(setup_quality=1), make_ctx(), limits)
    assert d.blocked_by == ["setup_quality"]


def test_t04_toxic_flow_blocked(limits):
    """목적: toxic_flow=true → 매수 금지
    입력: toxic_flow=True
    기대: approved=False"""
    d = evaluate_entry(make_signal(toxic_flow=True), make_ctx(), limits)
    assert d.blocked_by == ["toxic_flow"]


def test_t05_near_limit_blocked_model_and_deterministic(limits):
    """목적: risk_state=near_limit → 매수 금지(모델 값·결정론 값 각각)
    입력: 모델 risk_state=near_limit / 결정론 risk_state=near_limit
    기대: 둘 다 approved=False"""
    d1 = evaluate_entry(make_signal(risk_state="near_limit"), make_ctx(), limits)
    d2 = evaluate_entry(make_signal(), make_ctx(deterministic_risk_state="near_limit"), limits)
    assert d1.blocked_by == ["model_risk_state"]
    assert d2.blocked_by == ["deterministic_risk_state"]


def test_t06_calibration_invalid_blocked(limits):
    """목적: calibration_valid=false → 매수 금지
    입력: calibration_valid=False
    기대: approved=False"""
    d = evaluate_entry(make_signal(), make_ctx(calibration_valid=False), limits)
    assert d.blocked_by == ["calibration_valid"]


def test_t18_negative_net_ev_blocked(limits):
    """목적: 순기대값 음수·안전여유 미달·미산출 → 매수 금지
    입력: net_ev_bps=-5, 5(여유 10bps 미만), None
    기대: 모두 approved=False"""
    for ev in (-5.0, 5.0, None):
        d = evaluate_entry(make_signal(), make_ctx(net_ev_bps=ev), limits)
        assert d.blocked_by == ["net_expected_value"], ev


def test_t24_malformed_model_json_safe_neutral(limits):
    """목적: 비정상 모델 JSON → 안전한 관망
    입력: 깨진 JSON / 범위 밖 값 / 금지 필드(order_qty) / 문자열 bool
    기대: SafeNeutral 반환, 게이트 approved=False"""
    good = json.loads(make_signal().model_dump_json())
    cases = ["{not json", json.dumps({**good, "calibrated_prob": 1.7}), json.dumps({**good, "order_qty": 1000}),
             json.dumps({**good, "toxic_flow": "false"}), json.dumps([good]), ""]
    for raw in cases:
        r = parse_reflex_output(raw)
        assert isinstance(r, SafeNeutral), raw
        assert not evaluate_entry(r, make_ctx(), limits).approved
    assert not isinstance(parse_reflex_output(json.dumps(good)), SafeNeutral)


def test_expired_signal_blocked(limits):
    """목적: 허용 지연 초과 신호 폐기
    입력: now가 signal_valid_until 이후
    기대: signal_not_expired 실패"""
    s = make_signal()
    d = evaluate_entry(s, make_ctx(now=s.signal_valid_until), limits)
    assert d.blocked_by == ["signal_not_expired"]


def test_escalation_low_prob_and_crisis(limits):
    """목적: prob<0.60 또는 crisis → 진입 차단 + SYSTEM 2 상향
    입력: calibrated_prob=0.55 / regime=crisis
    기대: escalate_to_system2=True, approved=False"""
    d1 = evaluate_entry(make_signal(calibrated_prob=0.55), make_ctx(), limits)
    d2 = evaluate_entry(make_signal(regime="crisis"), make_ctx(), limits)
    assert d1.escalate_to_system2 and not d1.approved
    assert d2.escalate_to_system2 and not d2.approved and "regime_not_crisis" in d2.blocked_by


def test_missing_limits_blocks_everything():
    """목적: 위험 한도 설정 누락 → 신규 주문 금지
    입력: limits=None
    기대: approved=False"""
    assert not evaluate_entry(make_signal(), make_ctx(), None).approved
