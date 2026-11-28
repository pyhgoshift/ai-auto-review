"""한국투자증권 KIS Developers 어댑터 — [미구현 / 미검증].

GATE 3 이후, 공식 문서(apiportal.koreainvestment.com)와 모의투자 통합 테스트로 다음을 확인한 뒤 구현한다.
- OAuth 접근토큰 발급·만료·재발급 정책, 호출 제한(초당/일)
- 현금 주문/정정/취소 TR, 거래장소(KRX/NXT/SOR) 지정 방식
- 주문 응답 필드, 클라이언트 주문키(idempotency) 지원 여부 → 미지원 시 OMS 매칭 대사 유지
- 실시간 체결통보(WebSocket) 구독 방식, 재연결 시 누락 복구 방법
- 모의/실전 도메인과 자격증명 분리
어떤 엔드포인트·필드명도 문서 확인 없이 추측해 작성하지 않는다.
"""
from __future__ import annotations

from ..errors import NotVerifiedError


class KisBroker:
    name = "kis"

    def __init__(self, *_, **__):
        raise NotVerifiedError("KIS broker adapter not implemented: API not verified (GATE 3 required)")
