"""명시적 예외 계층. 모든 예외는 신규 위험 증가를 차단하는 방향으로 처리된다."""


class KqhError(Exception):
    """기본 예외."""


class ConfigError(KqhError):
    """설정 누락/형식 오류 → 신규 주문 금지."""


class NotVerifiedError(KqhError):
    """공식 문서·통합 테스트로 확인되지 않은 기능을 호출하려 할 때."""


class BrokerTimeout(KqhError):
    """주문 API 응답 없음. 주문 실패를 의미하지 않는다 → UNKNOWN + 대사."""


class BrokerRejected(KqhError):
    def __init__(self, reason: str):
        super().__init__(reason)
        self.reason = reason


class OrderPathViolation(KqhError):
    """승인 경로를 거치지 않은 주문 시도."""


class InvalidTransition(KqhError):
    """허용되지 않은 주문 상태 전이."""
