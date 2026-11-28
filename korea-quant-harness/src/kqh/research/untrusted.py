"""외부 텍스트(공시 본문·뉴스·모델 응답)는 '데이터'일 뿐 명령이 아니다.

- UntrustedText는 실행 가능한 메서드를 가지지 않는다.
- 의심 지시문을 탐지해 플래그로 남기되, 탐지 실패도 안전해야 한다:
  어떤 텍스트도 주문/설정/킬스위치 경로에 도달할 코드 경로가 없다(구조적 방어).
"""
from __future__ import annotations

import re
from dataclasses import dataclass

_PATTERNS = [
    r"ignore (all |any )?(previous|prior|above) (instructions|rules)",
    r"(이전|위의?) (지시|명령|규칙).{0,6}(무시|잊)",
    r"(submit|place|send)[ _-]?(an? )?(buy|sell|market)?[ _-]?order",
    r"(매수|매도) ?주문.{0,10}(실행|전송|넣)",
    r"(disable|turn off|release|해제).{0,12}(kill|킬|risk|리스크|한도)",
    r"system prompt|developer message|<\s*/?\s*system\s*>",
    r"(rm -rf|os\.system|subprocess|eval\(|exec\()",
]
_RE = [re.compile(p, re.IGNORECASE) for p in _PATTERNS]


@dataclass(frozen=True)
class UntrustedText:
    source_id: str
    text: str
    injection_suspected: bool
    matched: tuple[str, ...]

    def for_prompt(self) -> str:
        """모델 입력 시 데이터 경계로 감싼다. 경계 문자열 위조를 막기 위해 태그를 이스케이프."""
        safe = self.text.replace("<untrusted", "&lt;untrusted").replace("</untrusted", "&lt;/untrusted")
        return f'<untrusted_document source="{self.source_id}">\n{safe}\n</untrusted_document>'


def screen_untrusted(source_id: str, text: str) -> UntrustedText:
    m = tuple(p.pattern for p in _RE if p.search(text))
    return UntrustedText(source_id, text, bool(m), m)
