<#
.SYNOPSIS
  Installs the single-file AstraRouter desktop app for the current user.

.DESCRIPTION
  Copies AstraRouter.exe — one self-contained executable — to
  %LOCALAPPDATA%\AstraRouter (or -Target), pre-fetches the embedded database
  and runs migrations so the first launch is quick, then creates Start Menu /
  Desktop shortcuts and an Add/Remove-Programs entry. No admin rights, no
  Docker, no other files to manage: the app carries its runtime inside the
  .exe and extracts it on first run.

  The Docker deployment is a separate path and is never touched.
#>
[CmdletBinding()]
param(
  [string]$Target = (Join-Path $env:LOCALAPPDATA "AstraRouter"),
  [switch]$NoShortcuts,
  [switch]$NoStartup,
  [switch]$NoLaunch,
  [switch]$NoPrefetch,
  [switch]$Force
)

$ErrorActionPreference = "Stop"
$Bundle = $PSScriptRoot
$Exe = Join-Path $Bundle "AstraRouter.exe"

function Step($msg) { Write-Host "`n=== $msg === " -ForegroundColor Cyan }

if (-not (Test-Path $Exe)) { throw "AstraRouter.exe not found next to install.ps1 ($Bundle)" }

Step "install AstraRouter.exe -> $Target"
if ((Test-Path $Target) -and -not $Force) {
  if ((Get-ChildItem $Target -Force | Measure-Object).Count -gt 0) {
    throw "$Target exists and is not empty (re-run with -Force to reinstall)"
  }
}
New-Item -ItemType Directory $Target -Force | Out-Null
$TargetExe = Join-Path $Target "AstraRouter.exe"
Copy-Item $Exe $TargetExe -Force
foreach ($f in @("uninstall.ps1", "README.md", "VERSION.txt")) {
  $src = Join-Path $Bundle $f
  if (Test-Path $src) { Copy-Item $src (Join-Path $Target $f) -Force }
}
Write-Host "installed: $TargetExe"

if (-not $NoPrefetch) {
  Step "first-run setup (extract runtime, database, migrations)"
  $proc = Start-Process $TargetExe -ArgumentList @("--migrate-only", "--root", $Target) -Wait -PassThru -NoNewWindow
  if ($proc.ExitCode -ne 0) {
    # Not fatal: the app retries on launch and shows the reason itself.
    Write-Warning "first-run setup exited with code $($proc.ExitCode)"
    $boot = Join-Path $Target "logs\shell-bootstrap.log"
    if (Test-Path $boot) { Get-Content $boot | Select-Object -Last 10 }
    Write-Warning "continuing; the app will retry setup when you launch it."
  } else {
    Write-Host "runtime extracted, database ready, migrations applied"
  }
}

if (-not $NoShortcuts) {
  Step "shortcuts"
  $wsh = New-Object -ComObject WScript.Shell
  $icon = $TargetExe
  $sm = Join-Path ([Environment]::GetFolderPath("Programs")) "AstraRouter.lnk"
  $sc = $wsh.CreateShortcut($sm); $sc.TargetPath = $TargetExe; $sc.WorkingDirectory = $Target; $sc.IconLocation = "$TargetExe,0"; $sc.Save()
  $dt = Join-Path ([Environment]::GetFolderPath("Desktop")) "AstraRouter.lnk"
  $sc = $wsh.CreateShortcut($dt); $sc.TargetPath = $TargetExe; $sc.WorkingDirectory = $Target; $sc.IconLocation = "$TargetExe,0"; $sc.Save()
  Write-Host "Start Menu + Desktop shortcuts created"
}

if (-not $NoStartup) {
  Step "run at sign-in (per-user startup entry)"
  $startup = Join-Path ([Environment]::GetFolderPath("Startup")) "AstraRouter.lnk"
  $wsh = New-Object -ComObject WScript.Shell
  $sc = $wsh.CreateShortcut($startup); $sc.TargetPath = $TargetExe; $sc.WorkingDirectory = $Target; $sc.IconLocation = "$TargetExe,0"; $sc.Save()
  Write-Host "added to startup (disable in Task Manager > Startup if unwanted)"
}

Step "Add/Remove Programs entry (current user)"
$unkey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\AstraRouter"
New-Item $unkey -Force | Out-Null
Set-ItemProperty $unkey DisplayName "AstraRouter"
Set-ItemProperty $unkey DisplayVersion ((Get-Content (Join-Path $Target "VERSION.txt") -ErrorAction SilentlyContinue | Select-Object -First 1))
Set-ItemProperty $unkey InstallLocation $Target
Set-ItemProperty $unkey DisplayIcon "$TargetExe,0"
Set-ItemProperty $unkey UninstallString "powershell -ExecutionPolicy Bypass -File `"$Target\uninstall.ps1`""
Set-ItemProperty $unkey NoModify 1
Set-ItemProperty $unkey NoRepair 1

Write-Host "`nAstraRouter installed to $Target" -ForegroundColor Green
if (-not $NoLaunch) {
  Step "launch"
  Start-Process $TargetExe -WorkingDirectory $Target
  Write-Host "launched -- first visit shows the setup screen that creates the console administrator."
}
