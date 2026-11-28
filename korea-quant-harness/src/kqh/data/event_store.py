"""불변(append-only) 이벤트 저장소. JSONL + 해시 체인 + fsync.

기록 실패는 예외로 전파되어 호출자가 신규 주문을 차단한다(주문 의도는 '제출 전' 기록).
"""
from __future__ import annotations

import hashlib
import json
import os
from datetime import datetime
from decimal import Decimal
from enum import Enum
from pathlib import Path
from typing import Any, Iterator

GENESIS = "0" * 64


def _default(o: Any) -> Any:
    if isinstance(o, datetime):
        return o.isoformat()
    if isinstance(o, Decimal):
        return str(o)
    if isinstance(o, Enum):
        return o.value
    if hasattr(o, "model_dump"):
        return o.model_dump(mode="json")
    raise TypeError(f"not serializable: {type(o)}")


def canonical(obj: Any) -> str:
    return json.dumps(obj, sort_keys=True, separators=(",", ":"), default=_default, ensure_ascii=False)


def payload_hash(obj: Any) -> str:
    return hashlib.sha256(canonical(obj).encode()).hexdigest()


class EventStore:
    def __init__(self, path: str | Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._last_hash = GENESIS
        self._seq = 0
        for rec in self._iter_raw():
            self._last_hash, self._seq = rec["hash"], rec["seq"]

    def _iter_raw(self) -> Iterator[dict]:
        if not self.path.exists():
            return
        with self.path.open(encoding="utf-8") as f:
            for line in f:
                if line.strip():
                    yield json.loads(line)

    def append(self, event_type: str, data: dict) -> dict:
        body = {"seq": self._seq + 1, "type": event_type, "data": data, "prev": self._last_hash}
        h = hashlib.sha256(canonical(body).encode()).hexdigest()
        rec = {**json.loads(canonical(body)), "hash": h}
        with self.path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(rec, ensure_ascii=False, sort_keys=True) + "\n")
            f.flush()
            os.fsync(f.fileno())
        self._last_hash, self._seq = h, body["seq"]
        return rec

    def read(self, event_type: str | None = None) -> list[dict]:
        return [r for r in self._iter_raw() if event_type is None or r["type"] == event_type]

    def verify_chain(self) -> bool:
        prev = GENESIS
        for r in self._iter_raw():
            body = {k: r[k] for k in ("seq", "type", "data", "prev")}
            if r["prev"] != prev or hashlib.sha256(canonical(body).encode()).hexdigest() != r["hash"]:
                return False
            prev = r["hash"]
        return True
