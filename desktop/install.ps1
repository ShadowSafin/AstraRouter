<#
.SYNOPSIS
  Opens the AstraRouter setup program.

.DESCRIPTION
  AstraRouter ships as two programs. This script opens the installer, which
  installs the standalone application. After installation you launch the
  application (AstraRouter.exe, via the shortcut) — not this installer.

  .\AstraRouterSetup.exe   installer  (runs once, then exits)
  .\AstraRouter.exe        the app    (what you use every day)

  The Docker deployment is a separate path and is never touched.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$Setup = Join-Path $PSScriptRoot "AstraRouterSetup.exe"
if (-not (Test-Path $Setup)) {
  throw "AstraRouterSetup.exe was not found next to install.ps1 ($PSScriptRoot)."
}

Write-Host "Opening the AstraRouter setup wizard..." -ForegroundColor Cyan
Start-Process $Setup -WorkingDirectory $PSScriptRoot
