"""데이터 계약과 품질 검증. 판단 시점에 관측 가능했던 데이터만 사용(point-in-time)."""
from __future__ import annotations

import re
from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, field_validator, model_validator

QualityStatus = Literal["VALID", "SUSPECT", "INVALID", "UNVERIFIED"]
TICKER_RE = re.compile(r"^[0-9A-Z]{6}$")   # 6자리 영숫자 단축코드(신규 영문 포함 코드 대비) — 원천 문서로 확인 필요


class DataEnvelope(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    source_id: str
    instrument_id: str
    venue: Literal["KRX", "NXT", "NA"]
    event_time: datetime           # 사건 발생(관측 대상) 시각
    published_time: datetime       # 원천이 공개한 시각
    received_time: datetime        # 우리가 받은 시각
    available_at: datetime         # 전략이 사용할 수 있는 최초 시각 = max(published, received)
    source_sequence: int | None
    schema_version: str
    data_quality_status: QualityStatus
    payload_hash: str
    payload: dict

    @field_validator("event_time", "published_time", "received_time", "available_at")
    @classmethod
    def _aware(cls, v: datetime) -> datetime:
        if v.tzinfo is None:
            raise ValueError("timestamps must be timezone-aware")
        return v

    @model_validator(mode="after")
    def _causal(self) -> "DataEnvelope":
        if self.available_at < max(self.published_time, self.received_time):
            raise ValueError("available_at earlier than publication/receipt (look-ahead)")
        return self


def usable_at(env: DataEnvelope, decision_time: datetime) -> bool:
    """판단 시점 이전에 실제로 사용 가능했던 데이터인가? (T19)"""
    return env.data_quality_status == "VALID" and env.available_at <= decision_time


class StreamValidator:
    """중복·순서역전·종목코드 검증. 위반 이벤트는 반환값 False로 폐기한다."""

    def __init__(self) -> None:
        self._seen_hashes: set[str] = set()
        self._last_seq: dict[tuple[str, str], int] = {}
        self.issues: list[tuple[str, str]] = []

    def accept(self, env: DataEnvelope) -> bool:
        if env.venue != "NA" and not TICKER_RE.match(env.instrument_id):
            self.issues.append(("bad_ticker", env.instrument_id))
            return False
        if env.payload_hash in self._seen_hashes:
            self.issues.append(("duplicate", env.payload_hash))
            return False
        key = (env.source_id, env.instrument_id)
        if env.source_sequence is not None:
            last = self._last_seq.get(key)
            if last is not None and env.source_sequence <= last:
                self.issues.append(("out_of_order", f"{key}:{env.source_sequence}<={last}"))
                return False
            if last is not None and env.source_sequence > last + 1:
                self.issues.append(("sequence_gap", f"{key}:{last}->{env.source_sequence}"))
            self._last_seq[key] = env.source_sequence
        self._seen_hashes.add(env.payload_hash)
        return True
