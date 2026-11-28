"""fundamentals — [미구현]. 실데이터(DART/KRX/ECOS) 연동 전에는 분석 결과를 생성하지 않는다.
[실데이터 미연동 — 분석 불가]
"""
from __future__ import annotations

UNAVAILABLE = "[실데이터 미연동 — 분석 불가]"


def analyze(*_args, **_kw) -> str:
    return UNAVAILABLE
