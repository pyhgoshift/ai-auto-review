"""구조화(JSON) 로깅. 비밀값 키는 마스킹한다."""
from __future__ import annotations

import json
import logging

SECRET_KEYS = ("secret", "appkey", "app_key", "token", "password", "api_key", "authorization", "account_no")


def redact(d: dict) -> dict:
    return {k: ("***" if any(s in k.lower() for s in SECRET_KEYS) else v) for k, v in d.items()}


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        base = {"ts": self.formatTime(record), "lvl": record.levelname, "logger": record.name, "msg": record.getMessage()}
        extra = {k: v for k, v in record.__dict__.items() if k not in logging.LogRecord("", 0, "", 0, "", (), None).__dict__}
        return json.dumps({**base, **redact(extra)}, ensure_ascii=False, default=str)


def setup_logging(level: int = logging.INFO) -> None:
    h = logging.StreamHandler()
    h.setFormatter(JsonFormatter())
    root = logging.getLogger()
    root.handlers[:] = [h]
    root.setLevel(level)
