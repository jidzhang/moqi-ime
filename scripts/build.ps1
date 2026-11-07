#Requires -Version 5.1
<#
.SYNOPSIS
  Build the moqi-ime runtime tree for the installer (input_methods data,
  rime.dll, prebuilt dictionaries). The Go backend MoqiIME.exe retired
  2026-11-03: it is no longer built or shipped. Go is still required for
  the rime-prebuild dictionary precompile step.

.PARAMETER RepoRoot
  Root of moqi-ime (defaults to the parent directory of this script).

.PARAMETER BuildRoot
  Build output directory (default: scripts\build).

.PARAMETER PackageDir
  Packaged runtime directory (default: scripts\build\moqi-ime).
#>
param(
    [string] $RepoRoot = "",
    [string] $BuildRoot = "",
    [string] $PackageDir = ""
)

$ErrorActionPreference = "Stop"

function Write-Step {
    param([string] $Title)

    Write-Host ""
    Write-Host "============================================"
    Write-Host $Title
    Write-Host "============================================"
    Write-Host ""
}

function Ensure-Directory {
    param([string] $Path)

    New-Item -ItemType Directory -Path $Path -Force | Out-Null
}

function Remove-IfExists {
    param([string] $Path)

    if (Test-Path -LiteralPath $Path) {
        Remove-Item -LiteralPath $Path -Recurse -Force
    }
}

function Invoke-External {
    param(
        [string] $FilePath,
        [string[]] $ArgumentList,
        [switch] $IgnoreExitCode
    )

    Write-Host ">> $FilePath $($ArgumentList -join ' ')"
    & $FilePath @ArgumentList
    $exitCode = $LASTEXITCODE
    if (-not $IgnoreExitCode -and $exitCode -ne 0) {
        throw "Command failed with exit code ${exitCode}: $FilePath"
    }
    return $exitCode
}

function Copy-DirectoryContents {
    param(
        [string] $Source,
        [string] $Destination
    )

    Ensure-Directory -Path $Destination
    Copy-Item -Path (Join-Path $Source "*") -Destination $Destination -Recurse -Force
}

function Prepare-RimeData {
    param(
        [string] $RimeDataDir,
        [string] $PackageRimeDataDir
    )

    Remove-IfExists -Path $PackageRimeDataDir
    Ensure-Directory -Path $PackageRimeDataDir

    Write-Host "[INFO] Copying shared data from rime-frost submodule ..."
    Copy-DirectoryContents -Source $RimeDataDir -Destination $PackageRimeDataDir

    Remove-IfExists -Path (Join-Path $PackageRimeDataDir ".github")

    foreach ($name in @("README.md", "LICENSE")) {
        $path = Join-Path $PackageRimeDataDir $name
        if (Test-Path -LiteralPath $path) {
            Remove-Item -LiteralPath $path -Force
        }
    }

    Write-Host "[INFO] Packaged Rime shared data prepared at `"$PackageRimeDataDir`""
}

$scriptRepoRoot = Join-Path $PSScriptRoot ".."
if (-not $RepoRoot) { $RepoRoot = $scriptRepoRoot }
$RepoRoot = [System.IO.Path]::GetFullPath($RepoRoot)

if (-not $BuildRoot) { $BuildRoot = Join-Path $PSScriptRoot "build" }
if (-not $PackageDir) { $PackageDir = Join-Path $BuildRoot "moqi-ime" }

$BuildRoot = [System.IO.Path]::GetFullPath($BuildRoot)
$PackageDir = [System.IO.Path]::GetFullPath($PackageDir)
$InputMethodsDir = Join-Path $RepoRoot "input_methods"
$IconsDir = Join-Path $RepoRoot "icons"
$RimeDir = Join-Path $InputMethodsDir "rime"
$LibrimeBuildDll = Join-Path $RepoRoot "..\librime\build\bin\Release\rime.dll"
$LibrimeOctagramSrc = Join-Path $RepoRoot "..\librime\plugins\librime-octagram\src\octagram.cc"
$RimeDataDir = Join-Path $RepoRoot "rime-frost"
$PackageRimeDir = Join-Path $PackageDir "input_methods\rime"
$PackageRimeDataDir = Join-Path $PackageRimeDir "data"

Write-Host "============================================"
Write-Host " Moqi IME Runtime Build Script"
Write-Host "============================================"

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go was not found in PATH. Install Go from https://golang.org/dl/"
}

$goVersion = & go version
if ($LASTEXITCODE -ne 0) {
    throw "Failed to query Go version."
}
Write-Host "[INFO] Go version: $goVersion"

Write-Step -Title "Step 1: Prepare output directory"
if (Test-Path -LiteralPath $PackageDir) {
    Write-Host "[INFO] Removing old build output: `"$PackageDir`""
    Remove-Item -LiteralPath $PackageDir -Recurse -Force
}
Ensure-Directory -Path $PackageDir
Write-Host "[INFO] Output directory: `"$PackageDir`""

if (-not (Test-Path -LiteralPath (Join-Path $RimeDataDir "default.yaml"))) {
    throw "Missing rime-frost shared data submodule: `"$RimeDataDir`"`nRun: git submodule update --init --recursive rime-frost"
}

Push-Location $RepoRoot
try {
    Write-Step -Title "Step 2: Sync Go dependencies"
    $tidyExitCode = Invoke-External -FilePath "go" -ArgumentList @("mod", "tidy") -IgnoreExitCode
    if ($tidyExitCode -ne 0) {
        Write-Warning "go mod tidy failed, continuing..."
    }

    Write-Step -Title "Step 3: Copy packaged input_methods"
    if (-not (Test-Path -LiteralPath $RimeDir)) {
        throw "Missing Rime input method directory: `"$RimeDir`""
    }

    $packageInputMethodsDir = Join-Path $PackageDir "input_methods"
    Ensure-Directory -Path $packageInputMethodsDir
    Copy-DirectoryContents -Source $RimeDir -Destination (Join-Path $packageInputMethodsDir "rime")
    Write-Host "[INFO] Packaged only input_methods\rime"

    Write-Step -Title "Step 4: Copy shared icons"
    if (Test-Path -LiteralPath $IconsDir) {
        Copy-DirectoryContents -Source $IconsDir -Destination (Join-Path $PackageDir "icons")
        Write-Host "[INFO] icons copied"
    }
    else {
        Write-Warning "Missing icons directory: `"$IconsDir`""
    }

    Write-Step -Title "Step 5: Prepare packaged Rime shared data"
    Prepare-RimeData -RimeDataDir $RimeDataDir -PackageRimeDataDir $PackageRimeDataDir

    $sourceAppearanceThemes = Join-Path $RimeDir "appearance_themes.json"
    $packageAppearanceThemes = Join-Path $PackageRimeDir "appearance_themes.json"
    $packageAppearanceThemesData = Join-Path $PackageRimeDataDir "appearance_themes.json"
    if (-not (Test-Path -LiteralPath $sourceAppearanceThemes)) {
        throw "Missing builtin appearance themes file: `"$sourceAppearanceThemes`""
    }
    Copy-Item -LiteralPath $sourceAppearanceThemes -Destination $packageAppearanceThemes -Force
    Copy-Item -LiteralPath $sourceAppearanceThemes -Destination $packageAppearanceThemesData -Force
    Write-Host "[INFO] Copied appearance_themes.json into packaged Rime runtime"

    $sourceBigramTable = Join-Path $RimeDir "zh-bigram.tsv"
    if (-not (Test-Path -LiteralPath $sourceBigramTable)) {
        throw "Missing mixed-pair rerank table: `"$sourceBigramTable`""
    }
    Copy-Item -LiteralPath $sourceBigramTable -Destination (Join-Path $PackageRimeDataDir "zh-bigram.tsv") -Force
    Write-Host "[INFO] Copied zh-bigram.tsv into packaged Rime runtime"

    $pathsToRemove = @(
        @{ Path = Join-Path $PackageDir "input_methods\rime\data\others"; Label = "rime shared data others directory" },
        @{ Path = Join-Path $PackageDir "input_methods\rime\icons\icons"; Label = "nested icons directory" },
        # small gram ships with the rime-frost submodule but is excluded from
        # auto selection (grammar_model.go minAutoGramBytes = 64MB); never pack it
        @{ Path = Join-Path $PackageDir "input_methods\rime\data\zh-moqi.gram"; Label = "builtin small gram (inert, 7.3MB)" },
        # runtime reads the data/ copy only (bigram_rerank.go); the source copy
        # at the rime dir root must not ship
        @{ Path = Join-Path $PackageDir "input_methods\rime\zh-bigram.tsv"; Label = "duplicate zh-bigram.tsv at rime root" },
        # Android-only prebuilt libs from the upstream Android port: arm64 ELF,
        # never loaded on Windows. Keep them in the repo, drop from the package.
        @{ Path = Join-Path $PackageDir "input_methods\rime\android"; Label = "Android arm64 rime libs (6.1MB, unused on Windows)" },
        # Prebuild source directory: runtime copies already exist in data/
        # (lua/*.lua via patches, cn_dicts/moqi_bigram via import). The patches/
        # directory is the build-time source, not a runtime dependency.
        @{ Path = Join-Path $PackageDir "input_methods\rime\patches"; Label = "prebuild source patches (runtime copies in data/)" },
        # Calibration/test data, not needed at runtime
        @{ Path = Join-Path $PackageDir "input_methods\rime\calibration_pairs.txt"; Label = "calibration pairs (test data)" },
        @{ Path = Join-Path $PackageDir "input_methods\rime\calibration_sentences.txt"; Label = "calibration sentences (test data)" },
        # .gitignore from the submodule, meaningless in the install directory
        @{ Path = Join-Path $PackageDir "input_methods\rime\data\.gitignore"; Label = ".gitignore in install directory" },
        # Duplicate icons: moqi-ime/icons/* is a copy of input_methods/rime/icons/*
        # (verified byte-identical). The daemon/TSF reads from input_methods/rime/icons.
        @{ Path = Join-Path $PackageDir "icons"; Label = "duplicate icons directory (runtime reads input_methods/rime/icons)" }
    )
    foreach ($entry in $pathsToRemove) {
        if (Test-Path -LiteralPath $entry.Path) {
            Remove-Item -LiteralPath $entry.Path -Recurse -Force
            Write-Host "[INFO] Removed packaged $($entry.Label)"
        }
    }

    $packagedGoFiles = Get-ChildItem -LiteralPath (Join-Path $PackageDir "input_methods\rime") -Filter "*.go" -File -ErrorAction SilentlyContinue
    if ($packagedGoFiles) {
        $packagedGoFiles | Remove-Item -Force
        Write-Host "[INFO] Removed packaged Go source files"
    }

    $rimeDll = Join-Path $RimeDir "rime.dll"
    # Auto-sync rime.dll from the librime build output + freshness hard gate
    # (2026-10-25): a stale repo dll once shipped inside the installer because
    # the manual copy step was forgotten. The gate refuses to package an older
    # dll. Paths are resolved at script top-level next to $RepoRoot because
    # $PSScriptRoot-based derivation returned empty in this nested scope.
    if (Test-Path -LiteralPath $LibrimeBuildDll) {
        Copy-Item -LiteralPath $LibrimeBuildDll -Destination $rimeDll -Force
        Write-Host "[INFO] rime.dll synced from librime build output"
    }
    if (-not (Test-Path -LiteralPath $rimeDll)) {
        throw "rime.dll missing at '$rimeDll' (librime build output not found either)"
    }
    if ((Test-Path -LiteralPath $LibrimeOctagramSrc) -and
        (Get-Item -LiteralPath $LibrimeOctagramSrc).LastWriteTime -gt (Get-Item -LiteralPath $rimeDll).LastWriteTime) {
        throw "rime.dll is OLDER than octagram.cc - rebuild librime (rebuild-librime2.bat) before packaging"
    }
    Copy-Item -LiteralPath $rimeDll -Destination (Join-Path $PackageDir "input_methods\rime\rime.dll") -Force
    Write-Host "[INFO] Copied rime.dll into package output"

    Write-Step -Title "Step 6: Prebuild Rime dictionaries (first-key latency fix)"
    $prebuildStart = Get-Date
    & go run ./cmd/rime-prebuild -data "$PackageRimeDataDir"
    if ($LASTEXITCODE -ne 0) {
        throw "rime-prebuild failed with exit code $LASTEXITCODE"
    }
    Write-Host ("[INFO] Prebuild completed in {0:mm} min {0:ss} s" -f ((Get-Date) - $prebuildStart))
}
finally {
    Pop-Location
}

Write-Step -Title "Build completed"
Write-Host "Output directory:"
Write-Host "  `"$PackageDir`""
Write-Host ""
Write-Host "Packaging entry: moqi-im-windows\scripts\_all_in_package.ps1"
Write-Host "(stages this tree; MoqiIME.exe is retired and not part of it)"
