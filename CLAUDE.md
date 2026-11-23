# Claude 작업 메모

## last30days 리서치 스킬 (`.claude/skills/last30days`)

`/last30days <주제>`로 호출한다. 원본: mvanhorn/last30days-skill v4.6.0 (출처·업데이트: `.claude/skills/last30days/INSTALLED_FROM.md`).

### 클라우드 세션(claude.ai/code)에서 실행할 때
이 환경의 네트워크 정책은 Reddit·Hacker News·Polymarket·Wikidata·키 없는 웹검색(DDG/Brave/Startpage)을 403으로 막는다.
GitHub 검색(api.github.com/search)은 되고, 호스트 WebSearch 도구도 동작한다(단 reddit.com 도메인은 WebSearch에서도 제외됨).
따라서 아래 방식으로 실행한다.

1. 첫 실행 설정은 `.claude/last30days.env`(SETUP_COMPLETE=true, 브라우저 쿠키 읽기 끔)로 건너뛴다. 설정 마법사를 다시 돌리지 않는다.
2. WebSearch로 사전 조사(Step 0.55)를 하고, `LAST30DAYS_NATIVE_SEARCH=1`을 export한 뒤 직접 작성한 plan을 `--plan -` heredoc으로 넘긴다(LAW 7).
   - plan의 `sources`에는 `github`를 포함한다. 막힌 출처를 넣어도 실패만 기록될 뿐 치명적이지 않다.
3. 저장 위치를 남기고 싶으면 `--save-dir research/last30days`를 명시한다(클라우드의 홈 디렉터리는 세션 종료 시 사라짐).
4. 보고서에는 엔진의 `## Partial Coverage` 내용을 존중한다: 막힌 출처를 "언급 없음"으로 해석하지 않는다.

예:
```bash
export LAST30DAYS_NATIVE_SEARCH=1
python3 -I .claude/skills/last30days/scripts/last30days.py "주제" --emit=compact --save-dir research/last30days --plan - <<'PLAN_EOF'
{"intent":"product","freshness_mode":"strict_recent","cluster_mode":"story","subqueries":[{"label":"primary","search_query":"키워드","ranking_query":"자연어 질문?","sources":["github","reddit","hackernews","polymarket"],"weight":1.0}]}
PLAN_EOF
```

### 전체 출처를 쓰려면
- 로컬 PC의 Claude Code에서 이 레포를 열면 모든 무료 출처가 동작한다.
- 또는 클라우드 환경 설정에서 허용 호스트에 `www.reddit.com`, `reddit.com`, `hn.algolia.com`, `gamma-api.polymarket.com`, `www.wikidata.org`, `html.duckduckgo.com`을 추가한다.
- API 키(ScrapeCreators, xAI 등)는 이 레포 파일에 절대 쓰지 말고 환경의 보안 키(네트워크 시크릿)로 넣는다.
