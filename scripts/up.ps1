# Synapass stack restart (Windows PowerShell).
#
#   .\scripts\up.ps1           # detect the current LAN address, recreate the stack
#   .\scripts\up.ps1 gateway   # ...only one service (e.g. gateway, dashboard)
#
# Bare `docker compose up` reuses whatever GATEWAY_LAN_URL the containers were
# created with, which goes stale when the machine changes networks. This wrapper
# redetects the LAN IPv4 first and exports it for the compose invocation, so the
# Endpoints page always shows the current network's address. Use it instead of
# bare compose for every start/restart. Full checks live in deploy.ps1.
param([string]$Service = "")

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSCommandPath)
Set-Location -LiteralPath $RepoRoot

function Get-LanIPv4 {
  try {
    $Route = Get-NetRoute -DestinationPrefix "0.0.0.0/0" -AddressFamily IPv4 `
      -ErrorAction SilentlyContinue | Sort-Object RouteMetric | Select-Object -First 1
    if ($Route) {
      $Addr = Get-NetIPAddress -InterfaceIndex $Route.InterfaceIndex -AddressFamily IPv4 `
        -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -notmatch "^(127\.|169\.254\.)" } |
        Select-Object -First 1
      if ($Addr) { return $Addr.IPAddress }
    }
  } catch { }
  return ""
}

function Get-DotEnv([string]$Key, [string]$Default = "") {
  $EnvFile = Join-Path $RepoRoot ".env"
  if (Test-Path -LiteralPath $EnvFile) {
    $Line = Select-String -LiteralPath $EnvFile -Pattern "^$Key=" | Select-Object -Last 1
    if ($Line) {
      $Val = ($Line.Line -split "=", 2)[1]
      if ($Val -ne "") { return $Val }
    }
  }
  return $Default
}

if (-not [Environment]::GetEnvironmentVariable("GATEWAY_LAN_URL")) {
  $LanIP = Get-LanIPv4
  if ($LanIP) {
    $GwPort = Get-DotEnv "GATEWAY_PORT" "8080"
    $env:GATEWAY_LAN_URL = "http://${LanIP}:${GwPort}"
    Write-Host "LAN address: $env:GATEWAY_LAN_URL"
  } else {
    Write-Host "WARN: no LAN address detected; using .env as-is"
  }
} else {
  Write-Host "LAN address (pinned): $([Environment]::GetEnvironmentVariable('GATEWAY_LAN_URL'))"
}

if ($Service) { docker compose up -d $Service } else { docker compose up -d }
