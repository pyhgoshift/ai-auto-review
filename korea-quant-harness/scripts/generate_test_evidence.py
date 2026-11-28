"""pytest를 실제로 실행하고(JUnit XML) 테스트 docstring(목적/입력/기대)과 결합해 증거표를 만든다.

실행하지 않은 테스트는 PASS로 표시되지 않는다 — 결과는 오직 JUnit XML에서 온다.
  python scripts/generate_test_evidence.py  →  docs/test_evidence.md
"""
from __future__ import annotations

import ast
import platform
import re
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def docstrings() -> dict[tuple[str, str], dict[str, str]]:
    out = {}
    for f in (ROOT / "tests").rglob("test_*.py"):
        for n in ast.parse(f.read_text(encoding="utf-8")).body:
            if isinstance(n, ast.FunctionDef) and n.name.startswith("test_"):
                d = ast.get_docstring(n) or ""
                fields = dict(re.findall(r"^(목적|입력|기대):\s*(.+)$", d, re.M))
                out[(str(f.relative_to(ROOT)), n.name)] = fields
    return out


def main() -> int:
    with tempfile.TemporaryDirectory() as td:
        xml = Path(td) / "junit.xml"
        cmd = [sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider", f"--junitxml={xml}"]
        proc = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
        tree = ET.parse(xml)
    docs = docstrings()
    rows, totals = [], {"PASS": 0, "FAIL": 0, "SKIP": 0}
    for tc in tree.iter("testcase"):
        name = tc.get("name")
        base = name.split("[")[0]
        file = tc.get("classname").replace(".", "/") + ".py"
        res = "FAIL" if tc.find("failure") is not None or tc.find("error") is not None else \
              "SKIP" if tc.find("skipped") is not None else "PASS"
        totals[res] += 1
        m = re.match(r"test_(t\d{2}b?)_", base)
        tid = m.group(1).upper() if m else "-"
        d = docs.get((file, base), {})
        rows.append((tid, name, d.get("목적", ""), d.get("입력", ""), d.get("기대", ""), res, file))
    rows.sort(key=lambda r: (r[0] == "-", r[0], r[1]))
    now = datetime.now(timezone.utc).isoformat(timespec="seconds")
    lines = [
        "# 테스트 실행 증거 (자동 생성)", "",
        f"- 생성 시각(UTC): {now}", f"- Python: {platform.python_version()}",
        f"- 실행 명령: `{' '.join(['python', '-m', 'pytest', '-q'])}` (작업 디렉터리: `korea-quant-harness/`)",
        f"- 결과: PASS {totals['PASS']} / FAIL {totals['FAIL']} / SKIP {totals['SKIP']} (pytest 종료코드 {proc.returncode})",
        "- '실제 결과'는 JUnit XML에서만 가져온다. 이 문서는 **소프트웨어 동작**의 증거이며 **전략 수익성의 증거가 아니다**.",
        "", "| ID | 테스트 | 목적 | 입력 | 예상 결과 | 실제 결과 | 소스 |", "|---|---|---|---|---|---|---|",
    ]
    for r in rows:
        lines.append("| " + " | ".join(x.replace("|", "\\|") for x in r) + " |")
    (ROOT / "docs" / "test_evidence.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(proc.stdout.strip().splitlines()[-1])
    return proc.returncode


if __name__ == "__main__":
    raise SystemExit(main())
