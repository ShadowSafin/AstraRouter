<#
.SYNOPSIS
  Uninstalls AstraRouter desktop for the current user.

.DESCRIPTION
  Stops the app gracefully (WM_CLOSE first so services drain), removes the
  shortcuts and the Add/Remove-Programs entry, and deletes the install
  directory. Data is kept by default; pass -PurgeData to wipe the database
  too. Never touches Docker.
#>
[CmdletBinding()]
param(
  [string]$Target = (Join-Path $env:LOCALAPPDATA "AstraRouter"),
  [switch]$PurgeData
)

$ErrorActionPreference = "Continue"
$exe = Join-Path $Target "AstraRouter.exe"

Write-Host "=== stop AstraRouter ==="
$procs = Get-CimInstance Win32_Process -Filter "Name = 'AstraRouter.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($Target, [StringComparison]::OrdinalIgnoreCase) }
foreach ($p in $procs) {
  Write-Host "closing pid $($p.ProcessId) ..."
  Stop-Process -Id $p.ProcessId -ErrorAction SilentlyContinue
}
$deadline = (Get-Date).AddSeconds(30)
while ((Get-Date) -lt $deadline) {
  $left = Get-CimInstance Win32_Process -Filter "Name = 'AstraRouter.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($Target, [StringComparison]::OrdinalIgnoreCase) }
  if (-not $left) { break }
  Start-Sleep -Seconds 1
}
$left = Get-CimInstance Win32_Process -Filter "Name = 'AstraRouter.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($Target, [StringComparison]::OrdinalIgnoreCase) }
foreach ($p in $left) {
  Write-Warning "pid $($p.ProcessId) ignored close; killing"
  Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
}

Write-Host "=== shortcuts + registry ==="
Remove-Item (Join-Path ([Environment]::GetFolderPath("Programs")) "AstraRouter.lnk") -Force -ErrorAction SilentlyContinue
Remove-Item (Join-Path ([Environment]::GetFolderPath("Desktop")) "AstraRouter.lnk") -Force -ErrorAction SilentlyContinue
Remove-Item (Join-Path ([Environment]::GetFolderPath("Startup")) "AstraRouter.lnk") -Force -ErrorAction SilentlyContinue
Remove-Item "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\AstraRouter" -Recurse -Force -ErrorAction SilentlyContinue

Write-Host "=== files ==="
$data = Join-Path $Target "data"
if ((Test-Path $Target) -and -not $PurgeData -and (Test-Path $data)) {
  $keep = Join-Path ([IO.Path]::GetTempPath()) ("astrarouter-data-" + [Guid]::NewGuid().ToString("N"))
  Move-Item $data $keep
  Remove-Item $Target -Recurse -Force
  Write-Host "data kept at: $keep"
  Write-Host "(pass -PurgeData to wipe the database as well)"
} else {
  Remove-Item $Target -Recurse -Force
}
Write-Host "`nAstraRouter uninstalled." -ForegroundColor Green
