#Requires -Version 7.0
<#
.SYNOPSIS
    Assembles the autodeploy-desktop portable bundle (plan §8).

.DESCRIPTION
    1. Compiles autodeploy-desktop.exe  (GOOS=windows GOARCH=amd64)
    2. Compiles bin\wsl.exe             (cmd/wslshim — the wsl shim)
    3. Runs fetch-runtime.ps1           (pwsh portable + MSYS2 minimal runtime)
    4. Copies the pinned autodeploy.ps1 into autodeploy\ and writes .pinned-version
    5. Assembles the portable folder layout from plan §1
    6. Produces dist\autodeploy-desktop-<VERSION>-win-x64.zip

    NOTE: cmd/autodeploy-desktop and cmd/wslshim are authored by other workstreams
    and may not yet be present in all branches. This script references them by their
    final paths as defined in plan §1. The bundle assembly step is fully exercised
    only after all workstreams merge into main.

.PARAMETER Version
    The version string embedded in the binary via ldflags and used in the zip name.
    Defaults to the git tag/describe output, or "dev" if not in a git repo.

.PARAMETER AutodeployVersion
    The tag/branch of BaptisteTellier/autodeploy to fetch autodeploy.ps1 from.
    Defaults to "main". Pin to a specific tag (e.g. "v2.6.2") for releases.

.PARAMETER SkipRuntime
    Skip running fetch-runtime.ps1 (useful when runtime/ is already populated).

.PARAMETER SkipFetch
    Skip downloading autodeploy.ps1 (useful for offline/iterative builds).

.EXAMPLE
    .\scripts\build-bundle.ps1
    .\scripts\build-bundle.ps1 -Version v1.2.0 -AutodeployVersion v2.6.2
    .\scripts\build-bundle.ps1 -SkipRuntime
#>

[CmdletBinding()]
param(
    [string] $Version           = '',
    [string] $AutodeployVersion = 'main',
    [switch] $SkipRuntime,
    [switch] $SkipFetch
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# ── Resolve paths ─────────────────────────────────────────────────────────────
$RepoRoot    = $PSScriptRoot | Split-Path -Parent
$DistDir     = Join-Path $RepoRoot 'dist'
$BinDir      = Join-Path $RepoRoot 'bin'
$PwshDir     = Join-Path $RepoRoot 'pwsh'
$RuntimeDir  = Join-Path $RepoRoot 'runtime'
$AdDir       = Join-Path $RepoRoot 'autodeploy'
$FetchScript = Join-Path $PSScriptRoot 'fetch-runtime.ps1'

# Source paths (authored by other workstreams — referenced by final plan §1 paths).
$CmdDesktop  = Join-Path $RepoRoot 'cmd\autodeploy-desktop'
$CmdWslShim  = Join-Path $RepoRoot 'cmd\wslshim'

# ── Version ──────────────────────────────────────────────────────────────────
if (-not $Version) {
    try {
        $Version = (git -C $RepoRoot describe --tags --always --dirty 2>$null).Trim()
    } catch {}
    if (-not $Version) { $Version = 'dev' }
}
Write-Host "Building autodeploy-desktop $Version" -ForegroundColor Cyan

# ── Helpers ──────────────────────────────────────────────────────────────────
function Write-Step([string]$Msg) {
    Write-Host "  --> $Msg" -ForegroundColor Cyan
}
function Write-Done([string]$Msg) {
    Write-Host "  [OK] $Msg" -ForegroundColor Green
}

function Assert-Command([string]$Name) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required tool not found on PATH: $Name"
    }
}

# ── Prerequisites ─────────────────────────────────────────────────────────────
Assert-Command 'go'

# ── Step 1 : compile autodeploy-desktop.exe ──────────────────────────────────
Write-Host "`nStep 1/6 — Compiling autodeploy-desktop.exe" -ForegroundColor Yellow

if (-not (Test-Path $CmdDesktop)) {
    Write-Warning "cmd\autodeploy-desktop not found — skipping binary compilation."
    Write-Warning "This is expected before all workstreams merge. The bundle will be"
    Write-Warning "incomplete; rerun after other workstreams add cmd\autodeploy-desktop."
} else {
    Write-Step "go build cmd/autodeploy-desktop ..."
    $ldflags = "-s -w -X main.version=$Version"
    $env:GOOS   = 'windows'
    $env:GOARCH = 'amd64'
    & go build -ldflags $ldflags -o (Join-Path $RepoRoot 'autodeploy-desktop.exe') "$CmdDesktop"
    if ($LASTEXITCODE -ne 0) { throw "go build cmd/autodeploy-desktop failed" }
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Write-Done "autodeploy-desktop.exe compiled"
}

# ── Step 2 : compile bin\wsl.exe (wslshim) ───────────────────────────────────
Write-Host "`nStep 2/6 — Compiling bin\wsl.exe (wslshim)" -ForegroundColor Yellow

if (-not (Test-Path $CmdWslShim)) {
    Write-Warning "cmd\wslshim not found — skipping wsl.exe compilation."
    Write-Warning "This is expected before the wslshim workstream merges."
} else {
    Write-Step "go build cmd/wslshim ..."
    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    $env:GOOS   = 'windows'
    $env:GOARCH = 'amd64'
    & go build -ldflags "-s -w" -o (Join-Path $BinDir 'wsl.exe') "$CmdWslShim"
    if ($LASTEXITCODE -ne 0) { throw "go build cmd/wslshim failed" }
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Write-Done "bin\wsl.exe compiled"
}

# ── Step 3 : portable runtime (pwsh + MSYS2) ─────────────────────────────────
Write-Host "`nStep 3/6 — Portable runtime (fetch-runtime.ps1)" -ForegroundColor Yellow

if ($SkipRuntime) {
    Write-Host "  (SkipRuntime set — using existing pwsh/ and runtime/)" -ForegroundColor DarkGray
} else {
    & $FetchScript
    if ($LASTEXITCODE -ne 0) { throw "fetch-runtime.ps1 failed" }
}

# ── Step 4 : autodeploy.ps1 (pinned) ─────────────────────────────────────────
Write-Host "`nStep 4/6 — Fetching pinned autodeploy.ps1 ($AutodeployVersion)" -ForegroundColor Yellow

# SOURCE: BaptisteTellier/autodeploy releases on GitHub.
# The PS1 is fetched from the GitHub raw URL for the specified tag/branch.
# For production releases, set -AutodeployVersion to an exact tag (e.g. "v2.6.2").
# TODO: once autodeploy publishes a stable release cadence, pin to a specific
#       tag here and verify the SHA-256 of the downloaded PS1 (same pattern as
#       the pwsh zip above).

$AdPs1Url = "https://raw.githubusercontent.com/BaptisteTellier/autodeploy/${AutodeployVersion}/autodeploy.ps1"

New-Item -ItemType Directory -Force -Path $AdDir | Out-Null
$adPs1Dest = Join-Path $AdDir 'autodeploy.ps1'
$adVerDest = Join-Path $AdDir '.pinned-version'

if ($SkipFetch -and (Test-Path $adPs1Dest)) {
    Write-Host "  (SkipFetch set — using existing autodeploy.ps1)" -ForegroundColor DarkGray
} else {
    Write-Step "Downloading autodeploy.ps1 @ $AutodeployVersion ..."
    $ProgressPreference = 'SilentlyContinue'
    try {
        Invoke-WebRequest -Uri $AdPs1Url -OutFile $adPs1Dest -UseBasicParsing
    } catch {
        throw "Failed to download autodeploy.ps1 from $AdPs1Url : $_"
    }
    Set-Content -Path $adVerDest -Value $AutodeployVersion -Encoding UTF8
    Write-Done "autodeploy.ps1 @ $AutodeployVersion"
}

# ── Step 5 : assemble portable folder ────────────────────────────────────────
Write-Host "`nStep 5/6 — Assembling portable folder" -ForegroundColor Yellow

$BundleName = "autodeploy-desktop-${Version}-win-x64"
$BundleDir  = Join-Path $DistDir $BundleName
New-Item -ItemType Directory -Force -Path $BundleDir | Out-Null

# Helper: copy a path into the bundle, creating the parent directory.
function Add-ToBundle([string]$SourcePath, [string]$BundleRelPath, [string]$Description) {
    if (-not (Test-Path $SourcePath)) {
        Write-Warning "  MISSING: $Description ($SourcePath) — bundle will be incomplete."
        return
    }
    $dest = Join-Path $BundleDir $BundleRelPath
    New-Item -ItemType Directory -Force -Path (Split-Path $dest -Parent) | Out-Null
    if (Test-Path $SourcePath -PathType Container) {
        Copy-Item -Recurse -Force -Path $SourcePath -Destination $dest
    } else {
        Copy-Item -Force -LiteralPath $SourcePath -Destination $dest
    }
    Write-Done "  $Description -> $BundleRelPath"
}

# Layout per plan §1:
# autodeploy-desktop/
# ├─ autodeploy-desktop.exe
# ├─ WebView2Loader.dll         (optional — embedded by go-webview2 if available)
# ├─ pwsh/
# ├─ runtime/
# ├─ bin/
# │  └─ wsl.exe
# ├─ autodeploy/
# │  ├─ autodeploy.ps1
# │  └─ .pinned-version
# └─ data/                      (created at first launch — not bundled)

Add-ToBundle (Join-Path $RepoRoot 'autodeploy-desktop.exe')  'autodeploy-desktop.exe'  'main exe'
Add-ToBundle (Join-Path $RepoRoot 'WebView2Loader.dll')      'WebView2Loader.dll'      'WebView2Loader.dll (optional)'
Add-ToBundle $PwshDir                                         'pwsh'                    'PowerShell portable'
Add-ToBundle $RuntimeDir                                      'runtime'                 'MSYS2 runtime'
Add-ToBundle (Join-Path $BinDir 'wsl.exe')                   'bin\wsl.exe'             'wsl shim'
Add-ToBundle $AdDir                                           'autodeploy'              'autodeploy.ps1 + version'

# ── Step 6 : zip ─────────────────────────────────────────────────────────────
Write-Host "`nStep 6/6 — Creating zip archive" -ForegroundColor Yellow

New-Item -ItemType Directory -Force -Path $DistDir | Out-Null
$ZipPath = Join-Path $DistDir "${BundleName}.zip"

if (Test-Path $ZipPath) { Remove-Item -Force $ZipPath }

Write-Step "Compressing $BundleDir -> $ZipPath ..."
Compress-Archive -LiteralPath $BundleDir -DestinationPath $ZipPath -CompressionLevel Optimal
if (-not (Test-Path $ZipPath)) {
    throw "Zip creation failed: $ZipPath not found"
}

$zipSize = [math]::Round((Get-Item $ZipPath).Length / 1MB, 1)
Write-Done "dist\${BundleName}.zip ($zipSize MB)"

# ── Done ─────────────────────────────────────────────────────────────────────
Write-Host "`n=== build-bundle complete ===" -ForegroundColor Green
Write-Host "  Archive : dist\${BundleName}.zip"
Write-Host "  Version : $Version"
Write-Host ""
Write-Host "End-to-end validation (plan §10):" -ForegroundColor DarkCyan
Write-Host "  1. Extract the zip on a clean Windows VM" -ForegroundColor DarkCyan
Write-Host "  2. Double-click autodeploy-desktop.exe -> wizard window opens" -ForegroundColor DarkCyan
Write-Host "  3. Browse to a Veeam ISO -> follow wizard -> verify output in data\output\" -ForegroundColor DarkCyan
