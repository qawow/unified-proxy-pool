param(
    [string]$PanelHost = "0.0.0.0",
    [int]$PanelPort = 7891,
    [string]$DataDir = "",
    [string]$MihomoBinary = ""
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot

if (-not $DataDir) {
    $DataDir = Join-Path $RepoRoot "data"
}

$env:DATA_DIR = $DataDir
$env:PANEL_HOST = $PanelHost
$env:PANEL_PORT = "$PanelPort"

if ($MihomoBinary) {
    $env:MIHOMO_BINARY = $MihomoBinary
}

Push-Location $RepoRoot
try {
    Write-Host "DATA_DIR=$env:DATA_DIR"
    Write-Host "PANEL_HOST=$env:PANEL_HOST"
    Write-Host "PANEL_PORT=$env:PANEL_PORT"
    if ($env:MIHOMO_BINARY) {
        Write-Host "MIHOMO_BINARY=$env:MIHOMO_BINARY"
    } else {
        Write-Host "MIHOMO_BINARY=<auto-detect>"
    }
    go run ./cmd/app
} finally {
    Pop-Location
}
