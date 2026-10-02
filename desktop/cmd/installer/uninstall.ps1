# AstraRouter uninstaller (written by the installer next to AstraRouter.exe).
#
# Stops the app gracefully, removes its shortcuts and the Add/Remove Programs
# entry, and deletes the install folder. Your data is kept by default; pass
# -PurgeData to wipe the database as well. Docker is never touched.
[CmdletBinding()]
param(
  [string]$InstallDir = $PSScriptRoot,
  [string]$DataDir = "",
  [switch]$PurgeData
)

$ErrorActionPreference = "Continue"
$exe = Join-Path $InstallDir "AstraRouter.exe"
if (-not $DataDir) {
  $rootTxt = Join-Path $InstallDir "root.txt"
  if (Test-Path $rootTxt) { $DataDir = (Get-Content $rootTxt -Raw).Trim() } else { $DataDir = $InstallDir }
}

Write-Host "=== stop AstraRouter ==="
$procs = Get-CimInstance Win32_Process -Filter "Name = 'AstraRouter.exe'" -ErrorAction SilentlyContinue
foreach ($p in $procs) { Stop-Process -Id $p.ProcessId -ErrorAction SilentlyContinue }
$deadline = (Get-Date).AddSeconds(30)
while ((Get-Date) -lt $deadline) {
  if (-not (Get-CimInstance Win32_Process -Filter "Name = 'AstraRouter.exe'" -ErrorAction SilentlyContinue)) { break }
  Start-Sleep -Seconds 1
}
Get-CimInstance Win32_Process -Filter "Name = 'AstraRouter.exe'" -ErrorAction SilentlyContinue |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

Write-Host "=== shortcuts + registry ==="
$appdata = [Environment]::GetFolderPath("ApplicationData")
Remove-Item (Join-Path ([Environment]::GetFolderPath("Programs")) "AstraRouter.lnk") -Force -ErrorAction SilentlyContinue
Remove-Item (Join-Path ([Environment]::GetFolderPath("Desktop")) "AstraRouter.lnk") -Force -ErrorAction SilentlyContinue
Remove-Item (Join-Path $appdata "Microsoft\Windows\Start Menu\Programs\Startup\AstraRouter.lnk") -Force -ErrorAction SilentlyContinue
Remove-Item "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\AstraRouter" -Recurse -Force -ErrorAction SilentlyContinue

Write-Host "=== files ==="
if ($PurgeData) {
  Remove-Item $DataDir -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
} else {
  # Keep the database, logs and configuration.
  $dataInside = $DataDir.TrimEnd('\') -eq $InstallDir.TrimEnd('\')
  if ($dataInside) {
    Write-Host "data kept in $DataDir (pass -PurgeData to remove it)"
    Get-ChildItem $InstallDir -Force | Where-Object { $_.Name -notin @("data", "logs", "native.env", "config.yaml", "templates", "assets") } |
      Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
  } else {
    Remove-Item $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "data kept in $DataDir (pass -PurgeData to remove it)"
  }
}
Write-Host "`nAstraRouter uninstalled." -ForegroundColor Green
