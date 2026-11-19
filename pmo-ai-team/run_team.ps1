# PMO 주간보고 AI 팀 실행기 (폐쇄망 로컬 모델용)
# 사용법:  powershell -ExecutionPolicy Bypass -File .\run_team.ps1 -InputFile .\samples\가상_원자료_예시.txt -Date 2026-11-19
# 전제: llama-server가 http://127.0.0.1:8080 에서 실행 중 (local-llm-guide 참고)
# 이 스크립트는 파일을 읽고 out 폴더에 결과를 저장할 뿐, 발송·게시·삭제를 하지 않습니다.

param(
    [Parameter(Mandatory = $true)][string]$InputFile,
    [Parameter(Mandatory = $true)][string]$Date,
    [string]$Server = "http://127.0.0.1:8080",
    [int]$MaxRevisions = 2
)

$ErrorActionPreference = "Stop"
$Root = $PSScriptRoot
$Utf8 = New-Object System.Text.UTF8Encoding($false)

function Read-Text($path) { [System.IO.File]::ReadAllText($path, $Utf8) }
function Save-Text($path, $text) { [System.IO.File]::WriteAllText($path, $text, $Utf8) }
function Prompt-Of($name) { Read-Text (Join-Path $P $name) }
function Out-Of($name) { Join-Path $OutDir $name }

function Invoke-Agent([string]$SystemPrompt, [string]$UserText, [switch]$Json) {
    $body = @{
        model       = "local"
        messages    = @(
            @{ role = "system"; content = $SystemPrompt },
            @{ role = "user";   content = $UserText }
        )
        temperature = 0.2
        max_tokens  = 2500
        chat_template_kwargs = @{ enable_thinking = $false }
    }
    if ($Json) { $body.response_format = @{ type = "json_object" } }
    $bytes = $Utf8.GetBytes(($body | ConvertTo-Json -Depth 10))
    $resp = Invoke-WebRequest -Uri "$Server/v1/chat/completions" -Method Post -Body $bytes `
        -ContentType "application/json; charset=utf-8" -UseBasicParsing -TimeoutSec 1800
    $text = $Utf8.GetString($resp.RawContentStream.ToArray())
    return (($text | ConvertFrom-Json).choices[0].message.content).Trim()
}

function Get-JsonObject([string]$raw) {
    $s = $raw.IndexOf("{"); $e = $raw.LastIndexOf("}")
    if ($s -lt 0 -or $e -le $s) { throw "JSON을 찾지 못했습니다: $raw" }
    return $raw.Substring($s, $e - $s + 1)
}

# 서버 확인
try { Invoke-WebRequest -Uri "$Server/health" -UseBasicParsing -TimeoutSec 10 | Out-Null }
catch { Write-Host "로컬 AI 서버가 꺼져 있습니다. start.bat을 먼저 실행하세요." -ForegroundColor Red; exit 1 }

$OutDir = Join-Path (Join-Path $Root "out") ($Date + "_" + (Get-Date -Format "HHmmss"))
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$P = Join-Path $Root "prompts"
$Template = Read-Text (Join-Path (Join-Path $Root "templates") "주간보고_서식.md")
$Raw = Read-Text $InputFile

# 1) 정리 담당
Write-Host "[1/4] 정리 담당: 원자료 → JSON"
$facts = Invoke-Agent (Prompt-Of "01_정리담당.md") "[기준일]`n$Date`n`n[원자료]`n$Raw" -Json
if ($facts.StartsWith("STOP")) { Write-Host $facts -ForegroundColor Yellow; Save-Text (Out-Of "STOP.txt") $facts; exit 2 }
$facts = Get-JsonObject $facts
Save-Text (Out-Of "1_정리.json") $facts

# 2~3) 작성 담당 ↔ 검토 담당 (반려 시 최대 $MaxRevisions 회 수정)
$feedback = "없음"; $history = @(); $verdict = $null
for ($round = 0; $round -le $MaxRevisions; $round++) {
    Write-Host "[2/4] 작성 담당: 초안 작성 (회차 $round)"
    $draft = Invoke-Agent (Prompt-Of "02_작성담당.md") "[정리JSON]`n$facts`n`n[서식]`n$Template`n`n[반려사항]`n$feedback"
    Save-Text (Out-Of "2_초안_r$round.md") $draft

    Write-Host "[3/4] 검토 담당: 사실 대조"
    $review = Get-JsonObject (Invoke-Agent (Prompt-Of "03_검토담당.md") "[정리JSON]`n$facts`n`n[초안]`n$draft" -Json)
    Save-Text (Out-Of "3_검토_r$round.json") $review
    $verdict = $review | ConvertFrom-Json
    $history += "회차 ${round}: $($verdict.판정), 문제 $(@($verdict.문제).Count)건"
    if ($verdict.판정 -eq "통과") { break }
    $feedback = $review
}

# 4) 코디네이터: 사람에게 넘길 검토 요청서
Write-Host "[4/4] 코디네이터: 검토 요청서 작성"
$hist = ($history -join "`n") + "`n마지막 검토: $(Read-Text (Out-Of "3_검토_r$round.json"))"
$summary = Invoke-Agent (Prompt-Of "00_코디네이터.md") "[최종초안]`n$draft`n`n[검토이력]`n$hist`n`n[정리JSON]`n$facts"
Save-Text (Out-Of "4_검토요청서.md") $summary
Save-Text (Out-Of "최종초안.md") $draft

Write-Host "`n완료: $OutDir" -ForegroundColor Green
Write-Host "1) 4_검토요청서.md 를 먼저 읽고  2) 최종초안.md 를 검토한 뒤  3) 결재는 사람이 진행하십시오."
