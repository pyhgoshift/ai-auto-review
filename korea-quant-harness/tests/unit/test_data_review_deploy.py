"""데이터 시점 무결성(T19), 배포 게이트(T20), 보정 지표, 이벤트 저장소."""
import asyncio
import json
from datetime import timedelta

import pytest
from pydantic import ValidationError

from conftest import NOW
from kqh.data.event_store import EventStore, payload_hash
from kqh.data.validators import DataEnvelope, StreamValidator, usable_at
from kqh.data.websocket_manager import ConnState, ReconnectPolicy, WebSocketManager
from kqh.reflex.calibration import evaluate_calibration
from kqh.review.brier import brier_score, expected_calibration_error
from kqh.review.improvement_proposal import STAGES, ApprovalRecord, Proposal, StageResult, can_deploy
from kqh.review.nightly import build_reports


def env(**ov) -> DataEnvelope:
    pub = NOW + timedelta(hours=1)
    base = dict(source_id="DART", instrument_id="005930", venue="NA", event_time=NOW - timedelta(days=1),
                published_time=pub, received_time=pub + timedelta(seconds=3), available_at=pub + timedelta(seconds=3),
                source_sequence=1, schema_version="v1", data_quality_status="VALID", payload_hash="h1",
                payload={"title": "예시 공시 [예시 데이터]"})
    base.update(ov)
    return DataEnvelope(**base)


def test_t19_disclosure_not_usable_before_publication():
    """목적: 공시 공개 시각 이전에는 공시 데이터 사용 금지
    입력: 판단시각 10:00, 공시 공개 11:00(수신 11:00:03)
    기대: 10:00 사용 불가, 11:00:03 이후 사용 가능, available_at<published 생성 거부"""
    e = env()
    assert not usable_at(e, NOW)
    assert not usable_at(e, e.published_time)
    assert usable_at(e, e.available_at)
    with pytest.raises(ValidationError):
        env(available_at=NOW)
    assert not usable_at(env(data_quality_status="SUSPECT"), e.available_at + timedelta(days=1))


def test_stream_validator_dup_order_ticker():
    """목적: 중복·순서역전·잘못된 종목코드 폐기
    입력: 동일 해시 재수신, seq 역전, 'ABC' 코드
    기대: 각각 False"""
    v = StreamValidator()
    assert v.accept(env(venue="KRX", source_sequence=5, payload_hash="a"))
    assert not v.accept(env(venue="KRX", source_sequence=6, payload_hash="a"))
    assert not v.accept(env(venue="KRX", source_sequence=4, payload_hash="b"))
    assert not v.accept(env(venue="KRX", instrument_id="ABC", payload_hash="c"))
    assert v.accept(env(venue="KRX", source_sequence=9, payload_hash="d"))
    assert ("sequence_gap" in {i[0] for i in v.issues})


def _proposal(**stages) -> Proposal:
    st = {s: StageResult.PASS for s in STAGES[:-1]}
    st.update(stages)
    return Proposal(proposal_id="P1", description="threshold tweak", artifact_hash="abc", stages=st,
                    expands_risk_limits=False, n_variants_tried=12, created_at=NOW)


def test_t20_unapproved_model_version_not_deployable():
    """목적: 미승인 모델 버전 → 실거래 배포 금지
    입력: 승인 없음 / 해시 불일치 / PAPER 승인으로 LIVE 배포 / 섀도 테스트 미실행 / 위험한도 확대 미승인
    기대: 모두 False, 모든 단계 PASS + 해시 일치 LIVE 승인만 True"""
    p = _proposal()
    ok_live = ApprovalRecord(proposal_id="P1", artifact_hash="abc", operator_id="op", approved_at=NOW, scope="LIVE")
    assert not can_deploy(p, None, "LIVE")[0]
    assert "artifact_hash_mismatch" in can_deploy(p, ok_live.model_copy(update={"artifact_hash": "zzz"}), "LIVE")[1]
    assert not can_deploy(p, ok_live.model_copy(update={"scope": "PAPER"}), "LIVE")[0]
    assert not can_deploy(_proposal(SHADOW_TEST=StageResult.NOT_RUN), ok_live, "LIVE")[0]
    p2 = _proposal().model_copy(update={"expands_risk_limits": True})
    assert "risk_limit_expansion_not_approved" in can_deploy(p2, ok_live, "LIVE")[1]
    assert can_deploy(p, ok_live, "LIVE") == (True, [])


def test_brier_ece_and_calibration_validity():
    """목적: Brier/ECE 계산과 보정 유효성 판정(표본 부족·in-sample 거부)
    입력: 완전 예측, 과신 예측, 표본 100개
    기대: Brier 0, 과신 ECE 큼, 표본 부족·in-sample은 valid=False"""
    assert brier_score([1.0, 0.0], [1, 0]) == 0.0
    assert brier_score([0.9] * 10, [0] * 5 + [1] * 5) == pytest.approx(0.41)
    assert expected_calibration_error([0.9] * 10, [0] * 5 + [1] * 5) == pytest.approx(0.4)
    kw = dict(min_samples=500, max_ece=0.05, evaluated_at=NOW)
    assert evaluate_calibration("m", "def", [0.5] * 100, [0, 1] * 50, out_of_sample=True, **kw).reason == "insufficient_samples"
    assert not evaluate_calibration("m", "def", [0.5] * 1000, [0, 1] * 500, out_of_sample=False, **kw).valid
    assert evaluate_calibration("m", "def", [0.5] * 1000, [0, 1] * 500, out_of_sample=True, **kw).valid
    assert not evaluate_calibration("m", "def", [0.9] * 1000, [0, 1] * 500, out_of_sample=True, **kw).valid


def test_event_store_hash_chain_detects_tamper(tmp_path):
    """목적: 감사 로그 불변성 — 변조 탐지
    입력: 3건 기록 후 1건 수정
    기대: 변조 전 True, 후 False"""
    s = EventStore(tmp_path / "e.jsonl")
    for i in range(3):
        s.append("X", {"i": i})
    assert s.verify_chain()
    lines = (tmp_path / "e.jsonl").read_text().splitlines()
    rec = json.loads(lines[1]); rec["data"]["i"] = 99
    lines[1] = json.dumps(rec)
    (tmp_path / "e.jsonl").write_text("\n".join(lines) + "\n")
    assert not EventStore(tmp_path / "e.jsonl").verify_chain()
    assert payload_hash({"b": 1, "a": 2}) == payload_hash({"a": 2, "b": 1})


def test_nightly_includes_untraded_signals(tmp_path):
    """목적: 야간 평가가 매매하지 않은 신호도 포함(선택 편향 통제), 자동 배포 없음
    입력: traded 2건 + untraded 2건
    기대: n_untraded=2, auto_deploy=False"""
    s = EventStore(tmp_path / "e.jsonl")
    for i, (p, y, t) in enumerate([(0.9, 1, True), (0.85, 0, True), (0.3, 0, False), (0.7, 1, False)]):
        s.append("SIGNAL_OUTCOME", {"signal_id": f"s{i}", "regime": "trending", "predicted_prob": p,
                                    "outcome": y, "traded": t, "net_pnl": (100 if y else -80) if t else None})
    r = build_reports(s, NOW)
    assert r["CALIBRATION_REPORT"]["n_untraded"] == 2 and r["PROPOSED_IMPROVEMENTS"]["auto_deploy"] is False
    assert r["PNL_REPORT"]["n"] == 2


def test_websocket_bounded_reconnect_and_gap_flag():
    """목적: 제한된 재연결, gap 미복구 시 차단 상태 유지
    입력: 연결 계속 실패(시도 3회 제한) / 연결 성공 + gap 복구 실패
    기대: FAILED, gap 미복구 시 on_recovered 미호출"""
    calls = {"recovered": 0}

    async def fail(): raise ConnectionError()
    async def ok(): return None
    async def noop(): return None
    async def no_gap(): return False
    async def rec(): calls["recovered"] += 1
    async def nosleep(_): return None

    m = WebSocketManager(fail, noop, no_gap, rec, ReconnectPolicy(max_attempts=3), nosleep)
    assert asyncio.run(m.handle_disconnect()) == ConnState.FAILED and len(m.attempts_log) == 3
    m2 = WebSocketManager(ok, noop, no_gap, rec, ReconnectPolicy(max_attempts=3), nosleep)
    asyncio.run(m2.handle_disconnect())
    assert calls["recovered"] == 0 and "gap_not_recovered" in m2.attempts_log
