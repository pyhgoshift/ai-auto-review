"""KIS_MARKET_DATA 클라이언트 — [미구현 / 미검증].

공식 문서로 엔드포인트·필드·호출 제한·재배포 조건·과거 데이터 범위를 확인하기 전에는
어떤 요청/응답 모델도 추측해 작성하지 않는다. 확인 항목은 docs/api_inventory.md 참고.
"""
from __future__ import annotations

from ..errors import NotVerifiedError


class Client:
    def __init__(self, *_, **__):
        raise NotVerifiedError("kis_market_data: API contract not verified (see docs/api_inventory.md)")
