<#
.SYNOPSIS
  Opens the Synapass setup program.

.DESCRIPTION
  Synapass ships as two programs. This script opens the installer, which
  installs the standalone application. After installation you launch the
  application (Synapass.exe, via the shortcut) — not this installer.

  .\SynapassSetup.exe   installer  (runs once, then exits)
  .\Synapass.exe        the app    (what you use every day)

  The Docker deployment is a separate path and is never touched.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$Setup = Join-Path $PSScriptRoot "SynapassSetup.exe"
if (-not (Test-Path $Setup)) {
  throw "SynapassSetup.exe was not found next to install.ps1 ($PSScriptRoot)."
}

Write-Host "Opening the Synapass setup wizard..." -ForegroundColor Cyan
Start-Process $Setup -WorkingDirectory $PSScriptRoot
