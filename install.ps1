# code-graph installer (Windows). Needs only PowerShell 5.1+.
#
#   irm https://raw.githubusercontent.com/brandyn-s/code-graph/main/install.ps1 | iex
#
# Environment:
#   CODE_GRAPH_VERSION      release tag to install (default: latest, e.g. v0.9.0)
#   CODE_GRAPH_INSTALL_DIR  destination directory (default: %LOCALAPPDATA%\code-graph)
#   CODE_GRAPH_REPO         GitHub repository (default: brandyn-s/code-graph)
#
# The download is checked against the release's checksums.txt. If the GitHub CLI
# (gh) is installed and authenticated, build provenance is also verified with
# `gh attestation verify`; otherwise that step is skipped and reported. For the
# fully verified path, see scripts/setup-windows.ps1.
$ErrorActionPreference = "Stop"

$Repo = if ($env:CODE_GRAPH_REPO) { $env:CODE_GRAPH_REPO } else { "brandyn-s/code-graph" }
$InstallDir = if ($env:CODE_GRAPH_INSTALL_DIR) { $env:CODE_GRAPH_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "code-graph" }
$Version = if ($env:CODE_GRAPH_VERSION) { $env:CODE_GRAPH_VERSION } else { "latest" }
$Binary = "code-graph"
$Asset = "$Binary-windows-amd64.zip"

function Write-Ok($msg)   { Write-Host "  $msg" -ForegroundColor Green }
function Write-Warn($msg) { Write-Host "  $msg" -ForegroundColor Yellow }
function Fail($msg)       { Write-Host "  $msg" -ForegroundColor Red; exit 1 }

if ($env:PROCESSOR_ARCHITECTURE -ne "AMD64") {
    Fail "Only windows-amd64 release binaries are published. Build from source for $($env:PROCESSOR_ARCHITECTURE) (see CONTRIBUTING.md)."
}

$BaseUrl = if ($Version -eq "latest") {
    "https://github.com/$Repo/releases/latest/download"
} else {
    "https://github.com/$Repo/releases/download/$Version"
}

$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("code-graph-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null
try {
    Write-Host "Downloading $Asset ($Version) from $Repo..."
    $ZipPath = Join-Path $TmpDir $Asset
    $SumsPath = Join-Path $TmpDir "checksums.txt"
    Invoke-WebRequest -Uri "$BaseUrl/$Asset" -OutFile $ZipPath -UseBasicParsing
    Invoke-WebRequest -Uri "$BaseUrl/checksums.txt" -OutFile $SumsPath -UseBasicParsing

    $Expected = (Get-Content $SumsPath | Where-Object { $_ -match "\s\*?$([regex]::Escape($Asset))$" } | Select-Object -First 1) -split "\s+" | Select-Object -First 1
    if (-not $Expected) { Fail "checksums.txt has no entry for $Asset" }
    $Actual = (Get-FileHash -Algorithm SHA256 $ZipPath).Hash.ToLower()
    if ($Actual -ne $Expected.ToLower()) { Fail "Checksum mismatch for $Asset (expected $Expected, got $Actual)" }
    Write-Ok "SHA-256 verified"

    $gh = Get-Command gh -ErrorAction SilentlyContinue
    if ($gh) {
        & gh auth status *> $null
        if ($LASTEXITCODE -eq 0) {
            & gh attestation verify $ZipPath --repo $Repo *> $null
            if ($LASTEXITCODE -ne 0) { Fail "gh attestation verify failed for $Asset; refusing to install" }
            Write-Ok "Build provenance verified with gh attestation"
        } else {
            Write-Warn "Build provenance not verified (GitHub CLI not authenticated); checksum verification only"
        }
    } else {
        Write-Warn "Build provenance not verified (GitHub CLI not installed); checksum verification only"
    }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Expand-Archive -Path $ZipPath -DestinationPath $InstallDir -Force
    $BinaryPath = Join-Path $InstallDir "$Binary.exe"
    if (-not (Test-Path $BinaryPath)) { Fail "Archive did not contain $Binary.exe" }
    $VersionOut = & $BinaryPath --version
    Write-Ok "Installed $BinaryPath ($VersionOut)"

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
        Write-Ok "Added $InstallDir to your user PATH (restart the terminal to pick it up)"
    }

    Write-Host ""
    Write-Host "Next steps"
    Write-Host "  Claude Code:   claude mcp add code-graph --scope user -- `"$BinaryPath`""
    Write-Host "  Any client:    {""mcpServers"": {""code-graph"": {""command"": ""$($BinaryPath.Replace('\','\\'))""}}}"
    Write-Host "  Auto-configure supported clients:  & `"$BinaryPath`" install"
    Write-Host ""
    Write-Host "Semantic node search needs VOYAGE_API_KEY; everything else works offline."
    Write-Host "Docs: https://github.com/$Repo#readme"
}
finally {
    Remove-Item -Recurse -Force $TmpDir -ErrorAction SilentlyContinue
}
