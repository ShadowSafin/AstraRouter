<#
.SYNOPSIS
  Builds the single-file AstraRouter desktop app into desktop/dist/.

.DESCRIPTION
  Produces ONE self-contained executable, desktop/dist/AstraRouter.exe, with
  the gateway binary, a portable Node runtime, the built dashboard and the
  config templates embedded inside it via go:embed. Nothing needs to sit
  beside the .exe: it extracts its runtime to a per-user data directory on
  first launch. The installer (install.ps1) ships next to it and installs
  just that file.

  Docker files are never touched; this only reads the dashboard source and
  the main Go module to compile what the executable carries.

  Requirements: Go 1.24+, Node 20+ with npm, network access (node runtime
  download, Go modules).
#>
[CmdletBinding()]
param(
  [string]$Dist = (Join-Path $PSScriptRoot "dist"),
  [string]$NodeMajor = "22"
)

$ErrorActionPreference = "Stop"
$Desktop = $PSScriptRoot
$Repo = Split-Path $Desktop -Parent
$Payload = Join-Path $Desktop "internal\payload\files"
$Go = "C:\Program Files\Go\bin\go.exe"
if (-not (Test-Path $Go)) { $Go = "go" }

function Step($msg) { Write-Host "`n=== $msg === " -ForegroundColor Cyan }

Step "shell exe icon (rsrc)"
& $Go run github.com/akavel/rsrc@v0.10.2 -ico (Join-Path $Desktop "assets\icon.ico") -o (Join-Path $Desktop "cmd\shell\rsrc.syso")
if ($LASTEXITCODE -ne 0) { throw "rsrc failed" }

Step "gateway binary"
$Bin = Join-Path ([IO.Path]::GetTempPath()) "astrarouter-build"
if (Test-Path $Bin) { Remove-Item $Bin -Recurse -Force }
New-Item -ItemType Directory $Bin -Force | Out-Null
Push-Location $Repo
try {
  & $Go build -trimpath -o (Join-Path $Bin "astrarouter.exe") ./cmd/astrarouter
  if ($LASTEXITCODE -ne 0) { throw "gateway build failed" }
} finally { Pop-Location }

Step "dashboard build"
Push-Location (Join-Path $Repo "dashboard")
try {
  if (-not (Get-Command npm -ErrorAction SilentlyContinue)) { throw "npm not found (install Node.js 20+)" }
  npm ci --no-audit --no-fund
  if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
  npm run build
  if ($LASTEXITCODE -ne 0) { throw "dashboard build failed" }
} finally { Pop-Location }

Step "portable node runtime"
$index = Invoke-RestMethod "https://nodejs.org/dist/index.json"
$ver = ($index | Where-Object { $_.version -like "v$NodeMajor.*" } | Select-Object -First 1).version
if (-not $ver) { throw "no node v$NodeMajor found on nodejs.org" }
Write-Host "node $ver"
$zip = Join-Path ([IO.Path]::GetTempPath()) "node-$ver-win-x64.zip"
Invoke-WebRequest "https://nodejs.org/dist/$ver/node-$ver-win-x64.zip" -OutFile $zip
$unzip = Join-Path ([IO.Path]::GetTempPath()) "node-unzip"
if (Test-Path $unzip) { Remove-Item $unzip -Recurse -Force }
Expand-Archive $zip $unzip -Force
$NodeExe = Join-Path $unzip "node-$ver-win-x64\node.exe"
Copy-Item $NodeExe (Join-Path $Bin "node.exe") -Force
Remove-Item $zip -Force; Remove-Item $unzip -Recurse -Force

Step "stage embedded payload"
if (Test-Path $Payload) { Get-ChildItem $Payload -Force -Exclude ".keep" | Remove-Item -Recurse -Force }
New-Item -ItemType Directory $Payload -Force | Out-Null
$pBin = Join-Path $Payload "bin"
$pDash = Join-Path $Payload "dashboard"
$pStandalone = Join-Path $pDash ".next\standalone"
New-Item -ItemType Directory $pBin -Force | Out-Null
New-Item -ItemType Directory $pStandalone -Force | Out-Null
Copy-Item (Join-Path $Bin "astrarouter.exe") $pBin -Force
Copy-Item $NodeExe (Join-Path $pBin "node.exe") -Force
# Next resolves static assets relative to server.js, so they must live INSIDE
# the standalone dir: standalone/.next/static + standalone/public.
Copy-Item (Join-Path $Repo "dashboard\.next\standalone\*") $pStandalone -Recurse -Force
if (-not (Test-Path (Join-Path $pStandalone "server.js"))) { throw "standalone server.js missing after build" }
$pStatic = Join-Path $pStandalone ".next\static"
New-Item -ItemType Directory $pStatic -Force | Out-Null
Copy-Item (Join-Path $Repo "dashboard\.next\static\*") $pStatic -Recurse -Force
if (Test-Path (Join-Path $Repo "dashboard\public")) {
  $pPublic = Join-Path $pStandalone "public"
  New-Item -ItemType Directory $pPublic -Force | Out-Null
  Copy-Item (Join-Path $Repo "dashboard\public\*") $pPublic -Recurse -Force
}
Copy-Item (Join-Path $Desktop "assets") (Join-Path $Payload "assets") -Recurse -Force
Copy-Item (Join-Path $Desktop "templates") (Join-Path $Payload "templates") -Recurse -Force

$stamp = (Get-Date -Format o)
try { $stamp += " " + (git -C $Repo rev-parse --short HEAD 2>$null) } catch {}
Write-Host "payload stamp: $stamp"

Step "single-file AstraRouter.exe (payload embedded)"
Push-Location $Desktop
try {
  & $Go build ./...
  if ($LASTEXITCODE -ne 0) { throw "desktop go build failed" }
  if (Test-Path $Dist) { Remove-Item $Dist -Recurse -Force }
  New-Item -ItemType Directory $Dist -Force | Out-Null
  $vetted = "github.com/shadowsafin/astrarouter/desktop/internal/payload.version=$stamp"
  & $Go build -trimpath -ldflags "-H windowsgui -X `"$vetted`"" -o (Join-Path $Dist "AstraRouter.exe") ./cmd/shell
  if ($LASTEXITCODE -ne 0) { throw "shell build failed" }
} finally { Pop-Location }

Step "installer package"
foreach ($f in @("install.ps1", "uninstall.ps1", "README.md")) {
  Copy-Item (Join-Path $Desktop $f) (Join-Path $Dist $f) -Force
}
"desktop single-file app built $stamp" | Set-Content (Join-Path $Dist "VERSION.txt")
Remove-Item $Bin -Recurse -Force -ErrorAction SilentlyContinue

Write-Host "`napp ready: $(Join-Path $Dist 'AstraRouter.exe')" -ForegroundColor Green
Get-ChildItem $Dist -File | ForEach-Object { Write-Host ("  {0}  {1:N1} MB" -f $_.Name, ($_.Length / 1MB)) }
