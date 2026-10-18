#Requires -Version 5.1
<#
.SYNOPSIS
  一键复现墨奇输入法的全部评估数字（回归网 + 真机句子大集）。

.DESCRIPTION
  对标 google-input-tools 的 pinyin-engine/tools/eval-all.sh：
  回归网跨平台可跑；句子大集走真机 librime（Windows only），复用那边的
  zhihu3466 语料与 pinyin-data，保证整句准确率/CER 同口径可比。

.PARAMETER RepoRoot
  moqi-ime 仓库根（默认脚本所在目录的父目录）。

.PARAMETER Corpus
  金句语料（默认 ../google-input-tools/reference/corpus/eval-zhihu-3466.txt）。

.PARAMETER PinyinData
  mozillazg pinyin-data pinyin.txt（默认 ../google-input-tools/reference/pinyin-data/pinyin.txt）。

.PARAMETER RimeDataDir
  已部署的 rime data 目录（默认 scripts/build/moqi-ime/input_methods/rime/data，
  没有则退回已安装的 C:\Program Files (x86)\MoqiIM\moqi-ime\input_methods\rime\data）。

.PARAMETER Limit
  句子上限（默认 400；<=0 跑全量 3466）。
#>
param(
    [string] $RepoRoot = "",
    [string] $Corpus = "",
    [string] $PinyinData = "",
    [string] $RimeDataDir = "",
    [int] $Limit = 400
)

$ErrorActionPreference = "Stop"

if (-not $RepoRoot) { $RepoRoot = Split-Path -Parent $PSScriptRoot }
$RepoRoot = [System.IO.Path]::GetFullPath($RepoRoot)
$SiblingIME = Split-Path -Parent $RepoRoot
if (-not $Corpus) { $Corpus = Join-Path $SiblingIME "google-input-tools\reference\corpus\eval-zhihu-3466.txt" }
if (-not $PinyinData) { $PinyinData = Join-Path $SiblingIME "google-input-tools\reference\pinyin-data\pinyin.txt" }
if (-not $RimeDataDir) {
    $built = Join-Path $RepoRoot "scripts\build\moqi-ime\input_methods\rime\data"
    if (Test-Path -LiteralPath $built) {
        $RimeDataDir = $built
    } else {
        $RimeDataDir = "C:\Program Files (x86)\MoqiIM\moqi-ime\input_methods\rime\data"
    }
}
$UserDir = Join-Path ([System.IO.Path]::GetTempPath()) "moqi-eval-rime-user"
New-Item -ItemType Directory -Path $UserDir -Force | Out-Null

Push-Location $RepoRoot
try {
    Write-Host "== 回归网（跨平台） =="
    & go test ./eval/ -count=1
    if ($LASTEXITCODE -ne 0) { throw "eval regression failed" }

    Write-Host ""
    Write-Host "== 真机句子大集（keys 法） =="
    foreach ($p in @($Corpus, $PinyinData, $RimeDataDir)) {
        if (-not (Test-Path -LiteralPath $p)) { throw "missing eval input: $p" }
    }
    & go run ./eval/cmd/eval-sentences --corpus "$Corpus" --pinyin "$PinyinData" `
        --data "$RimeDataDir" --user "$UserDir" --limit $Limit
    if ($LASTEXITCODE -ne 0) { throw "native sentence eval failed" }
} finally {
    Pop-Location
}
