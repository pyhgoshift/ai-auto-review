# API 검증 목록 (PHASE 1 산출물)

> 상태 표기: **NOT_VERIFIED** = 공식 문서와 통합 테스트로 확인하지 않음. 이 문서에 있는 엔드포인트·필드명 **추측은 없다**. 아래 항목은 모두 "확인해야 할 질문"이다.
> 이번 작업에서는 **외부 API를 한 번도 호출하지 않았다.**

## 1. 한국투자증권 KIS Developers — `execution/kis_broker.py`, `data/kis_market_data.py`
| 확인 항목 | 왜 중요한가 | 상태 |
|---|---|---|
| 모의·실전 도메인 및 자격증명 분리 방식 | PAPER/LIVE 격리 | NOT_VERIFIED |
| 접근 토큰 발급·만료·재발급 제한 | 장중 인증 실패 | NOT_VERIFIED |
| REST 호출 제한(초당·일), WebSocket 구독 종목 수 제한 | 요청 큐·백오프 설계 | NOT_VERIFIED |
| 주문 시 거래장소 지정 방식(KRX/NXT/SOR) | 거래장소별 추적 | NOT_VERIFIED |
| **클라이언트 주문키(멱등성) 지원 여부** | 타임아웃 시 중복 방지 — 미지원이면 현재의 매칭 대사 방식 유지 | NOT_VERIFIED |
| 주문 조회 응답의 체결 수량, 평균가, 상태 필드 | `BrokerOrder` 매핑 | NOT_VERIFIED |
| 실시간 체결통보 방식과 재연결 시 누락 복구 방법 | 체결 누락 | NOT_VERIFIED |
| VI 중 허용되는 주문 유형, 거래정지 종목 주문 시 응답 | VI·정지 처리 | NOT_VERIFIED |
| 증권사 측 보호 기능(일괄 취소·주문 제한) 존재 여부 | BROKER_SIDE_PROTECTION — 현재 `broker_side_supported=False` | NOT_VERIFIED |
| 호가 10단계 실시간 제공 여부, 지연 | 상태 엔진 입력 | NOT_VERIFIED |

## 2. Open DART — `data/dart_client.py`
접수번호·접수시각(공개 시각)의 정밀도(일 단위인지 시각 단위인지), 정정공시 연결 방식, 재무제표 API의 최초 공시와 정정 구분, 호출 한도, 상업적 이용 조건. **공개 시각의 정밀도가 point-in-time 무결성의 핵심이다.** — NOT_VERIFIED

## 3. KRX — `data/krx_client.py`
투자자별 매매동향의 **공표 시각(장중 잠정 vs 장후 확정)**, 거래정지·관리종목 정보 제공 방식, 시점별 지수 구성 이력과 상장폐지 종목 포함 여부(생존편향), 자동 수집 허용 여부, 유료 데이터 여부. — NOT_VERIFIED

## 4. 한국은행 ECOS — `data/ecos_client.py`
통계별 공표 주기·공표 시각, 수정치(revision) 이력 제공 여부, 호출 한도. — NOT_VERIFIED

## 5. Anthropic Claude API (SYSTEM 2) — `research/brain.py`
| 항목 | 현재 파악(2026-11 문서 캐시 기준) | 확인 방법 |
|---|---|---|
| 모델 ID | 후보: `claude-opus-5-5` (기본 권장). 비용을 줄이려면 운영자가 Sonnet·Haiku 계열을 선택 | 런타임에 Models API(`models.retrieve`)로 확인한 뒤 `base.yaml brain.model_id`에 기입 |
| 가격 | Opus 5.5 입력 $4 / 출력 $20 per MTok(캐시 문서 기준) | 공식 가격 페이지로 재확인. **일일 비용 상한 설정 필수** |
| 구조화 출력 | `output_config.format` + `messages.parse()` 지원 | 통합 테스트 |
| 주의 | Opus 5.5는 thinking을 끌 수 없음(effort로 조절, 기본값 `medium`), 강제 `tool_choice` 미지원, `stop_reason: "refusal"` 처리 필요 | 통합 테스트 |
| 보안 | 공시·뉴스 본문은 `UntrustedText.for_prompt()`로 감싸서 전달. 모델 출력은 Pydantic으로 재검증 | T25 |
현재 `Brain`은 model_id, 검증 시각, 비용 상한 중 하나라도 없으면 생성 자체가 거부된다. — NOT_VERIFIED

## 6. 거래 제도·기준 데이터 (`configs/market_calendar.yaml`, `configs/costs.yaml`)
| 항목 | 현재 값의 출처 | 상태 |
|---|---|---|
| KRX/NXT 세션 시각(2026-09 개편 후) | 원본 명세의 "확인할 사항" 항목을 그대로 옮김 | NOT_VERIFIED |
| 호가단위표 | 2023-01-25 개편표로 알려진 값을 입력 | NOT_VERIFIED |
| 가격제한폭 30%, 상·하한가 계산 방식 | 일반적으로 알려진 값 | NOT_VERIFIED |
| 2026 매도세율(KOSPI 0.05%+농특세 0.15%, KOSDAQ 0.20%) | 원본 명세의 참고 기준 | NOT_VERIFIED |
| 수수료 1.5bps, 유관기관 비용 포함 여부 | 예시값 | NOT_VERIFIED |
| 2026 휴장일, 수능일 시간 변경 | 일부 예시 | NOT_VERIFIED |

## 7. 법·규제·약관 (명세에 빠져 있어 추가함)
- 증권사 Open API 이용약관에서 자동 주문을 허용하는 범위, 호출 빈도 제한, 계정 제재 조건
- 알고리즘·고빈도 거래와 관련된 거래소·감독당국의 신고·등록 의무 해당 여부
- 잦은 정정·취소가 허수성 호가나 시장교란 행위로 해석될 가능성 → 정정·취소 비율 상한을 정책으로 둘지 결정
- 데이터 재배포·상업적 이용 제한(KRX, 증권사 시세)
→ 전부 **NOT_VERIFIED**. 법률 판단은 이 시스템의 범위 밖이며, 운영자가 확인해야 한다.
