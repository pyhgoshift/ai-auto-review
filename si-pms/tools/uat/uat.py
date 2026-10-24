"""SI-PMS 실제 적용 테스트(UAT)

가상 프로젝트 하나를 브라우저로 처음부터 끝까지 운영하며 53개 항목을 확인한다.
새 데이터 폴더로 서버를 띄운 직후(관리자 비밀번호를 바꾸기 전)에 실행해야 한다.

    ./si-pms -addr 127.0.0.1:18091 -data uat-data > uat-server.log &
    pip install playwright && playwright install chromium   # 개발 PC에서 한 번
    SI_PMS_URL=http://127.0.0.1:18091 SI_PMS_LOG=uat-server.log python tools/uat/uat.py

결과는 SI_PMS_UAT_OUT(기본 ./uat-out)에 화면(shots/), 내려받은 파일(dl/), results.json으로 남는다.
"""
import os, re, sys, json, zipfile, csv, io, traceback
from pathlib import Path
from playwright.sync_api import sync_playwright

B = os.environ.get("SI_PMS_URL", "http://127.0.0.1:18091")
OUT = Path(os.environ.get("SI_PMS_UAT_OUT", "uat-out"))
SHOT, DL = OUT / "shots", OUT / "dl"
SHOT.mkdir(parents=True, exist_ok=True); DL.mkdir(parents=True, exist_ok=True)
PW = re.search(r"임시 비밀번호: (\S+)", Path(os.environ.get("SI_PMS_LOG", "uat-server.log")).read_text(encoding="utf-8")).group(1)
NEWPW = "Pmo-2026-Secure!"
PCS_CSV = Path(__file__).with_name("sample_pcs.csv")
CHROME = os.environ.get("CHROMIUM_PATH")  # 지정하지 않으면 Playwright 기본 브라우저
results = []


def check(name, ok, detail=""):
    results.append((name, bool(ok), detail))
    print(("통과" if ok else "실패"), name, detail if not ok else "")


def step(name):
    def deco(f):
        try:
            f()
        except Exception as e:
            check(name + " (예외)", False, f"{type(e).__name__}: {e}")
            traceback.print_exc()
    return deco


def shot(pg, name, full=True):
    pg.screenshot(path=str(SHOT / f"{name}.png"), full_page=full)


def flash(pg):
    el = pg.query_selector(".flash")
    return el.inner_text() if el else ""


def download(pg, url, name):
    with pg.expect_download() as d:
        pg.evaluate("u => { const a = document.createElement('a'); a.href = u; document.body.appendChild(a); a.click(); }", url)
    path = DL / name
    d.value.save_as(path)
    return path


with sync_playwright() as p:
    br = p.chromium.launch(executable_path=CHROME) if CHROME else p.chromium.launch()
    pc = br.new_context(viewport={"width": 1360, "height": 900}, accept_downloads=True)
    pg = pc.new_page()
    pg.on("dialog", lambda d: d.accept())

    @step("1. 첫 로그인과 비밀번호 변경")
    def _():
        pg.goto(B + "/login"); shot(pg, "01_로그인", False)
        pg.fill("input[name=login]", "admin"); pg.fill("input[name=password]", PW); pg.click("button.primary")
        check("임시 비밀번호 로그인 → 변경 화면 강제", "/password" in pg.url)
        pg.fill("input[name=current]", PW); pg.fill("input[name=new]", "short"); pg.fill("input[name=confirm]", "short"); pg.click("button.primary")
        check("약한 비밀번호 거절", "10자 이상" in pg.content())
        pg.fill("input[name=current]", PW); pg.fill("input[name=new]", NEWPW); pg.fill("input[name=confirm]", NEWPW); pg.click("button.primary")
        check("비밀번호 변경 후 대시보드", "/password" not in pg.url and "비밀번호를 변경했습니다" in flash(pg))
        pg.goto(B + "/password"); pg.fill("form[action='/profile'] input[name=name]", "박피엠"); pg.click("form[action='/profile'] button")
        check("PMO 이름 설정", "박피엠(으)로 바꿨습니다" in flash(pg), flash(pg))
        shot(pg, "01b_내정보", False)

    @step("2. 초기 설정")
    def _():
        pg.goto(B + "/settings")
        pg.fill("input[name=project_name]", "OO공단 차세대 시스템 구축")
        pg.fill("input[name=customer_name]", "OO공단")
        pg.fill("input[name=base_url]", B)
        pg.fill("textarea[name=allowed_companies]", "본사\n협력사A\n협력사B")
        pg.fill("textarea[name=ip_pool]", "10.10.5.0-10.10.5.255")
        pg.fill("input[name=ip_netmask]", "255.255.255.0")
        pg.fill("input[name=ip_gateway]", "10.10.5.1")
        pg.fill("input[name=ip_dns]", "10.10.0.10")
        pg.fill("textarea[name=ip_reserved]", "10.10.5.2-10.10.5.9")
        pg.fill("textarea[name=nac_csv_columns]", "MAC(하이픈)=mac\nIP=ip\n성명=owner\n소속=dept\n역할=role\n만료일시=unregdate")
        pg.fill("input[name=nac_role]", "PRJ-외부인력")
        pg.click("form[action='/settings'] button.primary")
        check("설정 저장", "설정을 저장했습니다" in flash(pg), flash(pg))
        shot(pg, "02_설정")
        # 잘못된 값은 전체 거절
        pg.fill("input[name=ip_gateway]", "10.10.5.300"); pg.click("form[action='/settings'] button.primary")
        check("잘못된 게이트웨이 거절·미저장", "저장하지 않았습니다" in flash(pg))

    @step("3. 서약·교육 문서 확인")
    def _():
        pg.goto(B + "/docs"); shot(pg, "03_서약교육", False)
        check("기본 문서 4종", all(t in pg.content() for t in ["개인정보 수집", "보안 서약서", "보안 교육", "철수 보안"]))

    @step("4. 회사 PC 일괄 등록(CSV)")
    def _():
        pg.goto(B + "/assets/new")
        pg.set_input_files("input[name=file]", str(PCS_CSV))
        pg.click("form[action='/assets/import'] button")
        check("CSV 3대 등록", "3건 등록" in flash(pg), flash(pg))
        shot(pg, "04_자산목록", False)
        check("MAC 표준화(00:50:56:AA:00:01)", "D7010-001" in pg.content())

    @step("5. 등록 QR 포스터")
    def _():
        pg.goto(B + "/qr"); shot(pg, "05_QR포스터", False)
        check("QR 이미지", pg.evaluate("document.querySelector('.qrimg').naturalWidth") > 100)

    join_url = pg.evaluate("fetch('/qr').then(r => r.text()).then(t => t.match(/(http[^<\" ]+join\\?c=[^<\" ]+)/)[1])").replace("&amp;", "&")
    phones = {}

    def join(name, company, phone, start, end, mac=None, serial=None, expect_ok=True, shot_name=None, label=None):
        ctx = br.new_context(viewport={"width": 400, "height": 860}, is_mobile=True)
        ph = ctx.new_page()
        ph.goto(join_url)
        if shot_name:
            shot(ph, shot_name)
        ph.fill("input[name=name]", name)
        ph.select_option("select[name=company]", company)
        ph.fill("input[name=phone]", phone)
        ph.fill("input[name=start_date]", start); ph.fill("input[name=end_date]", end)
        if mac:
            ph.check("input[value=byod]"); ph.fill("input[name=mac]", mac); ph.fill("input[name=serial]", serial or "SN")
            ph.fill("input[name=model]", "개인 노트북")
        else:
            ph.check("input[value=company]")
        for cb in ph.query_selector_all("input[type=checkbox]"):
            cb.check()
        ph.fill("input[name=signature]", name)
        ph.click("button.primary")
        ok = "등록이 접수되었습니다" in ph.content()
        check(label or f"QR 등록: {name}", ok == expect_ok, ph.inner_text(".flash") if ph.query_selector(".flash") else "")
        if ok:
            phones[name] = ph
        else:
            ctx.close()
        return ph

    @step("6. 투입 인력 QR 셀프 등록")
    def _():
        join("김개발", "협력사A", "010-1000-0001", "2026-10-20", "2027-03-31", "00-1A-2B-3C-4D-01", "LT-1001", shot_name="06_QR등록화면")
        shot(phones["김개발"], "07_등록완료_승인대기")
        join("이디자", "협력사B", "010-1000-0002", "2026-10-20", "2027-03-31", "00:1a:2b:3c:4d:01", expect_ok=False, label="MAC 중복 거절(이디자)")
        join("이디자", "협력사B", "010-1000-0002", "2026-10-20", "2027-03-31", "001A.2B3C.4D02", "LT-1002", label="QR 등록: 이디자(MAC 다른 표기)")
        join("최분석", "본사", "010-1000-0003", "2026-10-20", "2027-06-30")
        join("정테스트", "협력사A", "010-1000-0004", "2026-10-01", "2026-10-23", "00-1A-2B-3C-4D-04", "LT-1004", expect_ok=False, label="지난 철수예정일 거절")
        join("정테스트", "협력사A", "010-1000-0004", "2026-10-01", "2026-10-24", "00-1A-2B-3C-4D-04", "LT-1004")
        join("김개발", "협력사A", "010-1000-0001", "2026-10-20", "2027-03-31", expect_ok=False, label="같은 사람 중복 등록 거절")
        # 허용되지 않은 소속사는 선택 목록에 없음 → 직접 요청으로 확인
        r = pg.request.post(B + "/join", form={"c": join_url.split("c=")[1], "name": "한외부", "company": "외부업체", "phone": "010-9", "start_date": "2026-10-20", "end_date": "2027-01-01", "device": "company", "signature": "한외부"})
        check("허용되지 않은 소속사 거절", r.status == 400 and "허용된 소속사" in r.text())
        r = pg.request.get(B + "/join?c=wrongcode")
        check("잘못된 등록코드 거절", r.status == 404)

    @step("7. 대시보드 승인 대기열과 일괄 승인")
    def _():
        pg.goto(B + "/"); shot(pg, "08_대시보드_승인대기")
        check("승인 대기 4명", "승인 대기 4명" in pg.content())
        pg.click("text=일괄 승인")
        check("일괄 승인 4명", "4명 승인" in flash(pg), flash(pg))

    @step("8. 본인 화면: 승인 후 NAC 등록 전에는 IP 비공개")
    def _():
        ph = phones["김개발"]; ph.reload()
        check("NAC 등록 전 IP 비공개", "NAC 등록이 끝나면" in ph.content() and "10.10.5.10" not in ph.content())
        shot(ph, "09_본인_NAC대기")

    @step("9. 지급 PC 배정(최분석)")
    def _():
        pg.goto(B + "/people?status=active")
        pg.click("text=최분석")
        pg.select_option("select[name=asset_id]", pg.eval_on_selector("select[name=asset_id]", "s => [...s.options].find(o => o.text.includes('PC-001')).value"))
        pg.click("form[action$='/assign'] button")
        check("지급 PC 배정", "자산을 지급했습니다" in flash(pg), flash(pg))
        shot(pg, "10_인력상세")

    @step("10. IP 자동 배정 확인(예약 제외)")
    def _():
        pg.goto(B + "/nac"); shot(pg, "11_NAC_등록대기")
        reg = pg.inner_text("form:has(input[value=register])")
        ips = sorted(set(re.findall(r"10\.10\.5\.(\d+)", reg)), key=int)
        check("예약(.0~.9) 제외, .10부터 배정", ips[:4] == ["10", "11", "12", "13"], str(ips))
        check("등록 대기 4건(반입 3 + 지급 PC 1)", reg.count("10.10.5.") == 4, reg)

    @step("11. NAC 등록 파일·신청서 다운로드와 완료 처리")
    def _():
        f = download(pg, B + "/nac/export?kind=register&format=csv", "NAC_등록요청.csv")
        rows = list(csv.reader(io.StringIO(f.read_text(encoding="utf-8-sig"))))
        check("등록 CSV 머리글(고객사 양식)", rows[0] == ["mac", "ip", "owner", "dept", "role", "unregdate"], str(rows[0]))
        kim = [r for r in rows if r[2] == "김개발"]
        check("김개발 만료일시·MAC 표기", kim and kim[0][0] == "00-1A-2B-3C-4D-01" and kim[0][5] == "2027-03-31 23:59:59", str(kim))
        download(pg, B + "/nac/export?kind=register", "IP·NAC_등록_신청서.hwpx")
        pg.click("text=선택 건 NAC 등록 완료 처리")
        check("NAC 등록 완료 처리", "완료 처리했습니다" in flash(pg), flash(pg))

    @step("12. 본인 화면에 IP 공개")
    def _():
        ph = phones["김개발"]; ph.goto(join_url)  # QR 재스캔 → 내 상태 화면으로
        c = ph.content()
        check("QR 재스캔 시 상태 화면으로 이동", "진행 상태" in c)
        check("IP·게이트웨이·DNS 공개", "10.10.5.1" in c and "10.10.0.10" in c and "사용 가능" in c)
        shot(ph, "12_본인_IP확인")

    @step("13. 보안 점검과 계정 발급")
    def _():
        pg.goto(B + "/assets")
        for no in ["PC-001", "반입-0001", "반입-0002", "반입-0004"]:
            pg.goto(B + "/assets"); pg.click(f"text={no}")
            for cb in pg.query_selector_all("input[name=ok]"):
                cb.check()
            pg.click("form[action$='/check'] button")
        check("보안 점검 기록", "적합" in flash(pg), flash(pg))
        shot(pg, "13_자산상세_보안점검")
        pg.goto(B + "/people?status=active"); pg.click("text=김개발")
        for sysname, lid in [("VPN", "kim.dev"), ("형상관리(Git)", "kimdev")]:
            pg.fill("input[name=system]", sysname); pg.fill("input[name=login_id]", lid); pg.click("form[action$='/accounts'] button")
        check("계정 발급 기록", "계정 발급을 기록했습니다" in flash(pg))

    @step("14. 계약 연장 → NAC 만료일 변경 요청")
    def _():
        pg.fill("input[name=end_date]", "2027-06-30"); pg.click("form[action$='/edit'] button")
        pg.goto(B + "/")
        check("대시보드 만료일 변경 알림", "만료일 변경 대기 1건" in pg.content())
        pg.goto(B + "/nac")
        f = download(pg, B + "/nac/export?kind=update&format=csv", "NAC_만료일변경요청.csv")
        check("변경 CSV에 새 만료일", "2027-06-30 23:59:59" in f.read_text(encoding="utf-8-sig"))
        pg.click("text=선택 건 만료일 변경 완료 처리")
        check("만료일 변경 완료", "완료 처리했습니다" in flash(pg))

    @step("15. 조기 철수(이디자): 철수 시작 → 본인 철수서약 → NAC 해제 → 자동 완료")
    def _():
        pg.goto(B + "/people?status=active"); pg.click("text=이디자")
        pg.fill("input[name=actual_end_date]", "2026-10-24"); pg.click("form[action$='/offboard'] button")
        check("철수 시작", "철수 절차를 시작" in flash(pg), flash(pg))
        shot(pg, "14_철수체크리스트")
        ph = phones["이디자"]; ph.goto(join_url)
        check("본인 화면에 철수서약 표시", "철수 보안 서약서" in ph.content())
        shot(ph, "15_본인_철수서약")
        for cb in ph.query_selector_all("input[type=checkbox]"):
            cb.check()
        ph.fill("input[name=signature]", "이디자"); ph.click("button.primary")
        check("철수서약 제출", "철수 서약이 완료" in ph.content())
        pg.goto(B + "/nac")
        check("해제 대기에 이디자", "이디자" in pg.inner_text("form:has(input[value=remove])"))
        download(pg, B + "/nac/export?kind=remove&format=csv", "NAC_해제요청.csv")
        pg.click("text=선택 건 NAC 해제 완료 처리")
        p = pg.request.get(B + "/people?status=offboarded").text()
        check("체크리스트 충족 → 철수 자동 완료", "이디자" in p)

    @step("16. 철수예정일 경과 자동 철수(정테스트)")
    def _():
        pg.goto(B + "/people?status=active"); pg.click("text=정테스트")
        pg.fill("input[name=end_date]", "2026-10-23"); pg.click("form[action$='/edit'] button")
        pg.goto(B + "/jobs")
        pg.click("form:has(input[value=auto_offboard]) button")
        check("자동 철수 작업 실행", "1명 철수 진행" in flash(pg), flash(pg))
        shot(pg, "16_자동작업")
        pg.goto(B + "/nac")
        check("정테스트 NAC 해제 요청 자동 생성", "정테스트" in pg.inner_text("form:has(input[value=remove])"))

    @step("17. 대장 출력(HWP·CSV)과 개인별 문서")
    def _():
        pg.goto(B + "/exports"); shot(pg, "17_대장출력")
        for key, title in [("people", "인력투입대장"), ("assets", "PC자산대장"), ("pledges", "보안서약대장"), ("trainings", "보안교육대장"), ("accounts", "계정대장")]:
            download(pg, f"{B}/exports/{key}", f"{title}.hwpx")
            download(pg, f"{B}/exports/{key}?format=csv", f"{title}.csv")
        ids = re.findall(r"/people/(\d+)\"", pg.request.get(B + "/people?status=offboarded").text())
        download(pg, f"{B}/people/{ids[0]}/exit.hwpx", "철수확인서_이디자.hwpx")
        page = pg.request.get(f"{B}/people/{ids[0]}").text()
        ack = re.search(r"/acks/(\d+)/hwpx", page).group(1)
        download(pg, f"{B}/acks/{ack}/hwpx", "보안서약서_이디자.hwpx")
        z = download(pg, B + "/package", "제출패키지.zip")
        names = zipfile.ZipFile(z).namelist()
        check("제출 패키지 구성", len(names) >= 13 and "목록.txt" in names, str(len(names)))
        check("CSV 한글 머리글(엑셀 BOM)", (DL / "인력투입대장.csv").read_bytes().startswith(b"\xef\xbb\xbf"))
        check("대장 승인자에 PMO 실명", "박피엠" in (DL / "인력투입대장.csv").read_text(encoding="utf-8-sig"))

    @step("18. 고객사 보안담당 조회전용 계정")
    def _():
        pg.goto(B + "/settings")
        pg.fill("form[action='/users'] input[name=login]", "oo.security"); pg.fill("form[action='/users'] input[name=name]", "OO공단 보안담당")
        pg.select_option("form[action='/users'] select[name=role]", "viewer"); pg.click("form[action='/users'] button")
        tmp = re.search(r"임시 비밀번호: (\S+)", flash(pg)).group(1)
        v = br.new_context(viewport={"width": 1360, "height": 900}).new_page()
        v.goto(B + "/login"); v.fill("input[name=login]", "oo.security"); v.fill("input[name=password]", tmp); v.click("button.primary")
        v.fill("input[name=current]", tmp); v.fill("input[name=new]", "Viewer-2026!"); v.fill("input[name=confirm]", "Viewer-2026!"); v.click("button.primary")
        v.goto(B + "/people?status=active")
        check("조회전용: 목록 조회", "김개발" in v.content())
        check("조회전용: 등록 버튼 숨김", "투입 요청 등록" not in v.content())
        check("조회전용: 설정 접근 차단", v.goto(B + "/settings").status == 403)
        shot(v, "18_조회전용", False)

    @step("19. 감사 로그·백업·개인정보 파기")
    def _():
        pg.goto(B + "/audit?q=" + "승인"); shot(pg, "19_감사로그", False)
        check("감사 로그 한국어 검색", "투입 승인" in pg.content())
        download(pg, B + "/backup", "백업.db")
        pg.goto(B + "/people?status=offboarded"); pg.click("text=이디자"); pg.click("form[action$='/purge'] button")
        check("개인정보 파기", "파기했습니다" in flash(pg), flash(pg))

    @step("19b. 대리 등록(QR 불가 인원): 서면 서약 기록 후 승인")
    def _():
        pg.goto(B + "/people/new"); shot(pg, "21_대리등록", False)
        pg.fill("input[name=name]", "오서면"); pg.fill("input[name=company]", "본사")
        pg.fill("input[name=start_date]", "2026-10-24"); pg.fill("input[name=end_date]", "2027-01-31")
        pg.click("form[action='/people/new'] button.primary")
        check("대리 등록과 개인 링크 발급", pg.query_selector("#newlink") is not None)
        for label in ["개인정보 수집", "보안 서약서", "신규 투입자"]:
            pg.click("summary:has-text('서면 서약')")
            pg.select_option("select[name=document_id]", pg.eval_on_selector("select[name=document_id]", f"s => [...s.options].find(o => o.text.includes('{label}')).value"))
            pg.click("form[action$='/ack'] button")
        pg.click("form[action$='/approve'] button")
        check("서면 이행 후 승인", "승인 완료" in flash(pg), flash(pg))

    @step("20. 최종 대시보드")
    def _():
        pg.goto(B + "/"); shot(pg, "20_대시보드_최종")
        check("대시보드 렌더링", "지금 챙겨야 할 일" in pg.content())

    br.close()

fails = [r for r in results if not r[1]]
print(f"\n== 결과: {len(results) - len(fails)}/{len(results)} 통과")
for n, _, d in fails:
    print("실패:", n, d)
json.dump(results, open(OUT / "results.json", "w", encoding="utf-8"), ensure_ascii=False, indent=1)
sys.exit(1 if fails else 0)
