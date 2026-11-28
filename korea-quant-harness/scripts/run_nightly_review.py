"""야간 리뷰: 이벤트 저장소에서 보고서를 생성(외부 연결 없음)."""
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
from kqh.data.event_store import EventStore  # noqa: E402
from kqh.review.nightly import build_reports  # noqa: E402

store = EventStore(sys.argv[1] if len(sys.argv) > 1 else "run/events.jsonl")
print(json.dumps(build_reports(store, datetime.now(timezone.utc)), ensure_ascii=False, indent=2))
