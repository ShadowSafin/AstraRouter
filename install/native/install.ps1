<#
.SYNOPSIS
  Native install helper (Windows): prerequisites, build, configure, migrate.

.DESCRIPTION
  Thin wrapper over `synapass native install`, which owns every real step so
  the scripted path and the manual path cannot drift apart. Run from the
  repository root:

    powershell -ExecutionPolicy Bypass -File install/native/install.ps1

  Arguments are passed through, e.g. -skip-dashboard-build, -with-workers.
  See `synapass native install --help` and documentation/installation/native.md.
#>
[CmdletBinding()]
param(
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$NativeArgs
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent)

function Need($Name, $Hint) {
  if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
    Write-Error "missing required tool: $Name ($Hint)"
    exit 1
  }
}

Need "go" "install Go 1.27+ (https://go.dev/dl)"
if (($NativeArgs -notcontains "--skip-dashboard-build") -and ($NativeArgs -notcontains "--skip-dashboard")) {
  Need "node" "install Node.js 20+ (https://nodejs.org)"
  Need "npm" "install Node.js 20+ (https://nodejs.org)"
}

if (-not (Test-Path "go.mod")) {
  Write-Error "run this script from the Synapass repository root"
  exit 1
}

# Build the installer binary first so the remaining steps run from a real
# install rather than `go run` (identical code, stable process identity for
# the supervisor's self-reference).
& go build -trimpath -o bin/synapass.exe ./cmd/synapass
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& .\bin\synapass.exe native install @NativeArgs
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host ""
Write-Host "Datastores still need to be reachable (PostgreSQL, Redis, ClickHouse and"
Write-Host "NATS are host services in native mode - see the Datastores section of"
Write-Host "documentation/installation/native.md), then:"
Write-Host ""
Write-Host "  .\bin\synapass.exe native up"
