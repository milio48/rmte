# RMTE Windows Portable Download Script
# Usage:
#   irm https://raw.githubusercontent.com/milio48/rmte/main/install/install.ps1 | iex
#   or:
#   curl.exe -fsSL https://raw.githubusercontent.com/milio48/rmte/main/install/install.ps1 -o install.ps1; powershell -ExecutionPolicy Bypass -File install.ps1; Remove-Item install.ps1

$ErrorActionPreference = "Stop"

Write-Host "==> Downloading RMTE portable binary for Windows..." -ForegroundColor Cyan

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
$targetExe = Join-Path (Get-Location).Path "rmte.exe"
$tempExe = Join-Path (Get-Location).Path "rmte-download.tmp"

Write-Host "==> Detected platform: windows/$arch" -ForegroundColor Cyan
Write-Host "==> Downloading $downloadUrl..." -ForegroundColor Cyan

try {
    # Prefer curl.exe if available (faster and handles progress cleanly)
    if (Get-Command curl.exe -ErrorAction SilentlyContinue) {
        & curl.exe -fL --progress-bar "$downloadUrl" -o "$tempExe"
        if ($LASTEXITCODE -ne 0) {
            throw "curl.exe exited with code $LASTEXITCODE"
        }
    } else {
        # Fallback to Invoke-WebRequest
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri $downloadUrl -OutFile $tempExe -UseBasicParsing
    }
} catch {
    if (Test-Path $tempExe) { Remove-Item $tempExe -Force }
    Write-Error "Failed to download RMTE binary from $downloadUrl : $_"
    exit 1
}

# Move temporary file to final target
Move-Item -Path $tempExe -Destination $targetExe -Force

Write-Host "==> Successfully downloaded portable binary to $targetExe" -ForegroundColor Green
Write-Host "`nRun '.\rmte.exe help' or '.\rmte.exe serve' to get started.`n" -ForegroundColor Green
Write-Host "Tip: To make it available globally, optionally move rmte.exe to a folder in your PATH.`n" -ForegroundColor Yellow
