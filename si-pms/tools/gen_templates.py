"""기본 HWPX 양식 생성기 (개발용).

운영 환경(폐쇄망)에서는 필요 없습니다. 생성된 hwpx/*.hwpx 파일이 실행파일에 내장됩니다.
양식 문구나 열 구성을 바꾸고 싶을 때만 아래처럼 다시 생성하세요.

    pip install python-hwpx
    python tools/gen_templates.py

자리표시자 규칙
  {{키}}          단일 값 (예: {{프로젝트명}})
  {{목록.필드}}    표의 행 안에 두면 목록 항목 수만큼 그 행이 복제됩니다 (예: {{인력.성명}})
"""
from pathlib import Path

from hwpx import HwpxDocument

OUT = Path(__file__).resolve().parent.parent / "hwpx"
HEADER_SHADE = "#D9E2F3"


class Builder:
    def __init__(self, landscape=False):
        self.doc = HwpxDocument.new()
        self.doc.page.setup(
            paper_size="A4",
            orientation="landscape" if landscape else "portrait",
            margins_mm={"left": 15, "right": 15, "top": 15, "bottom": 15},
        )
        self.title_style = self.doc.styles.ensure_run(bold=True, size=18)
        self.bold = self.doc.styles.ensure_run(bold=True)
        self.count = 1  # 빈 문서에 기본 문단 1개(구역 정의)가 있음

    def para(self, text="", style=None, align=None, after=None):
        self.doc.add_paragraph(text, char_pr_id_ref=style)
        if align or after is not None:
            kw = {}
            if align:
                kw["alignment"] = align
            if after is not None:
                kw["spacing_after_pt"] = after
            self.doc.set_paragraph_format(paragraph_index=self.count, **kw)
        self.count += 1

    def title(self, text):
        self.para(text, self.title_style, align="CENTER", after=8)

    def table(self, rows, weights=None, header_rows=(0,), label_cols=()):
        t = self.doc.add_table(len(rows), len(rows[0]))
        for r, row in enumerate(rows):
            for c, text in enumerate(row):
                t.set_cell_text(r, c, text)
                if r in header_rows or c in label_cols:
                    t.set_cell_shading(r, c, HEADER_SHADE)
        if weights:
            t.set_column_widths(weights)
        self.count += 1
        return t

    def save(self, name):
        report = self.doc.validate()
        if report.issues:
            raise SystemExit(f"{name}: {report.issues}")
        self.doc.save_to_path(OUT / name)
        print("wrote", OUT / name)


def ledger(name, title, list_name, cols, weights, landscape=True):
    b = Builder(landscape)
    b.title(title)
    b.para("프로젝트: {{프로젝트명}}    고객사: {{고객사}}    기준일: {{출력일}}", align="RIGHT", after=4)
    b.table(
        [[c for c in cols], ["{{%s.%s}}" % (list_name, c) for c in cols]],
        weights,
    )
    b.para("")
    b.para("작성자: {{출력자}}            확인자:                    (서명)", align="RIGHT")
    b.save(name)


def main():
    OUT.mkdir(exist_ok=True)
    ledger(
        "people.hwpx", "인력 투입 관리 대장", "인력",
        ["번호", "성명", "소속", "직급", "담당업무", "연락처", "투입일", "철수예정일", "철수일", "상태", "출입증"],
        [5, 9, 12, 7, 14, 12, 10, 10, 10, 7, 8],
    )
    ledger(
        "assets.hwpx", "PC·자산 관리 대장", "자산",
        ["번호", "자산번호", "종류", "모델", "시리얼", "IP", "사용자", "사용자소속", "지급일", "최근점검일", "점검결과"],
        [5, 11, 8, 12, 12, 11, 8, 11, 10, 10, 8],
    )
    ledger(
        "pledges.hwpx", "보안서약 관리 대장", "서약",
        ["번호", "성명", "소속", "문서명", "버전", "서명일", "방식"],
        [6, 10, 14, 26, 6, 14, 8], landscape=False,
    )
    ledger(
        "trainings.hwpx", "보안교육 관리 대장", "교육",
        ["번호", "성명", "소속", "교육명", "이수일", "유효기한", "방식"],
        [6, 10, 14, 24, 12, 12, 8], landscape=False,
    )
    ledger(
        "accounts.hwpx", "계정 관리 대장", "계정",
        ["번호", "성명", "소속", "시스템", "계정ID", "발급일", "회수일"],
        [6, 10, 14, 16, 16, 12, 12], landscape=False,
    )

    # IP·NAC 등록/해제 신청서 (NAC 담당자 제출용)
    b = Builder(landscape=True)
    b.title("IP·NAC {{신청구분}} 신청서")
    b.para("프로젝트: {{프로젝트명}}    고객사: {{고객사}}    신청일: {{출력일}}    건수: {{건수}}건", align="RIGHT", after=4)
    b.para("네트워크 설정 — 서브넷: {{서브넷}}   게이트웨이: {{게이트웨이}}   DNS: {{DNS}}", after=4)
    cols = ["번호", "신청구분", "MAC", "IP", "성명", "소속", "자산번호", "장비", "시리얼", "사용시작", "사용종료"]
    b.table([cols, ["{{장비.%s}}" % c for c in cols]], [5, 7, 14, 11, 8, 11, 10, 10, 11, 9, 9])
    b.para("")
    b.para("위와 같이 업무망 IP 사용 및 NAC {{신청구분}}을 신청합니다.", align="CENTER")
    b.para("신청자: {{출력자}}            NAC 담당 확인:                    (서명)", align="RIGHT")
    b.save("nac_request.hwpx")

    # 개인별 보안서약서
    b = Builder()
    b.title("{{문서명}}")
    b.para("{{본문}}", after=10)
    b.table(
        [["성명", "{{성명}}", "소속", "{{소속}}"],
         ["직급", "{{직급}}", "담당업무", "{{담당업무}}"],
         ["서명일시", "{{서명일시}}", "서명방식", "{{서명방식}}"],
         ["문서버전", "{{버전}}", "본문 해시", "{{본문해시}}"]],
        [12, 38, 12, 38], header_rows=(), label_cols=(0, 2),
    )
    b.para("")
    b.para("위 내용을 충분히 이해하였으며 이를 준수할 것을 서약합니다.", align="CENTER")
    b.para("{{서명일}}", align="CENTER")
    b.para("서약자: {{성명}} (전자서명: {{서명이름}})", align="RIGHT")
    b.para("{{프로젝트명}} 귀중", b.bold, align="CENTER")
    b.save("pledge_doc.hwpx")

    # 개인별 철수 확인서
    b = Builder()
    b.title("철수 보안 확인서")
    b.table(
        [["성명", "{{성명}}", "소속", "{{소속}}"],
         ["투입일", "{{투입일}}", "철수일", "{{철수일}}"],
         ["출입증", "{{출입증}}", "출입증 반납", "{{출입증반납일}}"]],
        [14, 36, 14, 36], header_rows=(), label_cols=(0, 2),
    )
    b.para("")
    b.para("1. 지급 자산 반납 내역", b.bold)
    b.table(
        [["번호", "자산번호", "종류", "모델", "시리얼", "지급일", "반납일"],
         ["{{자산.번호}}", "{{자산.자산번호}}", "{{자산.종류}}", "{{자산.모델}}",
          "{{자산.시리얼}}", "{{자산.지급일}}", "{{자산.반납일}}"]],
        [6, 16, 10, 16, 18, 14, 14],
    )
    b.para("")
    b.para("2. 계정 회수 내역", b.bold)
    b.table(
        [["번호", "시스템", "계정ID", "발급일", "회수일"],
         ["{{계정.번호}}", "{{계정.시스템}}", "{{계정.계정ID}}", "{{계정.발급일}}", "{{계정.회수일}}"]],
        [8, 26, 26, 20, 20],
    )
    b.para("")
    b.para("3. 철수 서약 확인", b.bold)
    b.table(
        [["번호", "문서명", "서명일", "방식"],
         ["{{서약.번호}}", "{{서약.문서명}}", "{{서약.서명일}}", "{{서약.방식}}"]],
        [8, 52, 24, 16],
    )
    b.para("")
    b.para("위 인원은 프로젝트 철수 시 지급 자산과 계정을 모두 반납·회수하였음을 확인합니다.", align="CENTER")
    b.para("{{출력일}}", align="CENTER")
    b.para("확인자: {{출력자}}                    (서명)", align="RIGHT")
    b.save("exit_doc.hwpx")


if __name__ == "__main__":
    main()
