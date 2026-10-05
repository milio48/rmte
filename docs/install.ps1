# RMTE Windows Installer & Launcher
# Usage:
#   irm https://rmte.biz.id/install.ps1 | iex                                              # Default: Download to current dir
#   & ([scriptblock]::Create((irm https://rmte.biz.id/install.ps1))) run                   # Download to %TEMP% and run
#   & ([scriptblock]::Create((irm https://rmte.biz.id/install.ps1))) download              # Download to current dir
#   & ([scriptblock]::Create((irm https://rmte.biz.id/install.ps1))) install               # Install to ~/.local/bin and update PATH
#   & ([scriptblock]::Create((irm https://rmte.biz.id/install.ps1))) download run          # Download to current dir and run
#   & ([scriptblock]::Create((irm https://rmte.biz.id/install.ps1))) install run           # Install to bin and run

[CmdletBinding()]
param(
    [Parameter(Position=0, ValueFromRemainingArguments=$true)]
    [string[]]$Actions
)

$ErrorActionPreference = "Stop"

# Collect actions from param, $args, or $env:RMTE_ACTION
if (-not $Actions -or $Actions.Count -eq 0) {
    if ($args -and $args.Count -gt 0) {
        $Actions = $args
    } elseif ($env:RMTE_ACTION) {
        $Actions = $env:RMTE_ACTION -split '\s+'
    } else {
        $Actions = @("download")
    }
}

$doDownload = $false
$doInstall = $false
$doRun = $false
$extraArgs = @()

foreach ($action in $Actions) {
    switch ($action.ToLower()) {
        "run"      { $doRun = $true }
        "download" { $doDownload = $true }
        "install"  { $doInstall = $true }
        default    { $extraArgs += $action }
    }
}

# Determine target directory
if (-not $doDownload -and -not $doInstall -and $doRun) {
    $targetDir = [System.IO.Path]::GetTempPath()
} elseif ($doInstall) {
    $targetDir = Join-Path $env:USERPROFILE ".local\bin"
} else {
    $targetDir = (Get-Location).Path
}

if (-not (Test-Path $targetDir)) {
    New-Item -ItemType Directory -Path $targetDir -Force | Out-Null
}

$targetExe = Join-Path $targetDir "rmte.exe"
$tempExe = Join-Path $targetDir ("rmte.tmp." + [System.Guid]::NewGuid().ToString("N").Substring(0, 8) + ".exe")

Write-Host "==> Preparing RMTE binary for Windows..." -ForegroundColor Cyan

# Detect Architecture
$arch = $env:PROCESSOR_ARCHITECTURE
switch -Regex ($arch) {
    "AMD64|x86_64" { $arch = "amd64" }
    "ARM64"        { $arch = "arm64" }
    default {
        Write-Error "Unsupported architecture: $arch. Only 64-bit (amd64 / arm64) is supported."
        exit 1
    }
}

$binaryName = "rmte-windows-$arch.exe"
$downloadUrl = "https://github.com/milio48/rmte/releases/latest/download/$binaryName"

Write-Host "==> Detected platform: windows/$arch" -ForegroundColor Cyan
Write-Host "==> Downloading $downloadUrl..." -ForegroundColor Cyan

try {
    # Prefer curl.exe if available (clean progress indicator)
    if (Get-Command curl.exe -ErrorAction SilentlyContinue) {
        & curl.exe -fL --progress-bar "$downloadUrl" -o "$tempExe"
        if ($LASTEXITCODE -ne 0) {
            throw "curl.exe exited with code $LASTEXITCODE"
        }
    } else {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri $downloadUrl -OutFile $tempExe -UseBasicParsing
    }
} catch {
    if (Test-Path $tempExe) { Remove-Item $tempExe -Force -ErrorAction SilentlyContinue }
    Write-Error "Failed to download RMTE binary from $downloadUrl : $_"
    exit 1
}

# Move temporary file to final target
Move-Item -Path $tempExe -Destination $targetExe -Force

Write-Host "==> Ready: $targetExe" -ForegroundColor Green

# If install mode, check and add to User PATH
if ($doInstall) {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$targetDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$targetDir;$userPath", "User")
        $env:Path = "$targetDir;$env:Path"
        Write-Host "==> Added $targetDir to your User PATH environment variable." -ForegroundColor Cyan
    }
}

# Run binary if requested
if ($doRun) {
    Write-Host "`n==> Launching RMTE...`n" -ForegroundColor Green
    if ($extraArgs.Count -gt 0) {
        & $targetExe @extraArgs
    } else {
        & $targetExe
    }
} else {
    Write-Host "`nRun '$targetExe help' or '$targetExe' to get started.`n" -ForegroundColor Green
}
