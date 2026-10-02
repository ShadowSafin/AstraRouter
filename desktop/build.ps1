<#
.SYNOPSIS
  Builds the AstraRouter desktop executables into desktop/dist/.

.DESCRIPTION
  Produces TWO separate programs, as different products:

    dist/AstraRouter.exe       the standalone app (runtime only)
    dist/AstraRouterSetup.exe  the setup-only installer

  The installer embeds the standalone app plus its runtime (gateway, portable
  Node, built dashboard, templates) and installs them. The app embeds no
  installer: it only runs what is installed.

  Docker files are never touched; this only reads the dashboard source and the
  main Go module.

  Requirements: Go 1.24+, Node 20+ with npm, network access.
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

Step "app + installer icons (rsrc)"
& $Go run github.com/akavel/rsrc@v0.10.2 -ico (Join-Path $Desktop "assets\icon.ico") -o (Join-Path $Desktop "cmd\app\rsrc.syso")
if ($LASTEXITCODE -ne 0) { throw "rsrc (app) failed" }
& $Go run github.com/akavel/rsrc@v0.10.2 -ico (Join-Path $Desktop "assets\icon.ico") -o (Join-Path $Desktop "cmd\installer\rsrc.syso")
if ($LASTEXITCODE -ne 0) { throw "rsrc (installer) failed" }

$Bin = Join-Path ([IO.Path]::GetTempPath()) "astrarouter-build"
if (Test-Path $Bin) { Remove-Item $Bin -Recurse -Force }
New-Item -ItemType Directory $Bin -Force | Out-Null
# NOTE: the app and gateway MUST stage in separate directories. "AstraRouter.exe"
# (app) and "astrarouter.exe" (gateway) are the same filename on Windows'
# case-insensitive filesystem, so building both into $Bin flat makes the gateway
# silently overwrite the app and the installer ships the gateway as the app
# (double-click then flashes a console and exits).
$BinApp = Join-Path $Bin "app-stage"
$BinGw = Join-Path $Bin "gw-stage"
New-Item -ItemType Directory $BinApp, $BinGw -Force | Out-Null

Step "standalone app exe (runtime, no payload)"
if (Test-Path $Dist) {
  try {
    Remove-Item $Dist -Recurse -Force -ErrorAction Stop
  } catch {
    # A stray directory handle (Explorer, AV scan) can block removing the
    # folder itself; empty it instead and carry on.
    Get-ChildItem $Dist -Force -ErrorAction Stop | Remove-Item -Recurse -Force -ErrorAction Stop
  }
}
New-Item -ItemType Directory $Dist -Force | Out-Null
Push-Location $Desktop
try {
  & $Go build ./...
  if ($LASTEXITCODE -ne 0) { throw "desktop go build failed" }
  & $Go build -trimpath -ldflags "-H windowsgui" -o (Join-Path $BinApp "AstraRouter.exe") ./cmd/app
  if ($LASTEXITCODE -ne 0) { throw "app build failed" }
  Copy-Item (Join-Path $BinApp "AstraRouter.exe") (Join-Path $Dist "AstraRouter.exe") -Force
} finally { Pop-Location }

Step "gateway binary"
Push-Location $Repo
try {
  & $Go build -trimpath -o (Join-Path $BinGw "astrarouter.exe") ./cmd/astrarouter
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
Remove-Item $zip -Force

Step "stage installer payload"
if (Test-Path $Payload) { Get-ChildItem $Payload -Force -Exclude ".keep" | Remove-Item -Recurse -Force }
New-Item -ItemType Directory $Payload -Force | Out-Null
$pBin = Join-Path $Payload "bin"
$pStandalone = Join-Path $Payload "dashboard\.next\standalone"
New-Item -ItemType Directory $pBin -Force | Out-Null
New-Item -ItemType Directory $pStandalone -Force | Out-Null
Copy-Item (Join-Path $BinGw "astrarouter.exe") (Join-Path $pBin "astrarouter.exe") -Force
Copy-Item $NodeExe (Join-Path $pBin "node.exe") -Force
# The standalone app is what the installer installs.
Copy-Item (Join-Path $BinApp "AstraRouter.exe") (Join-Path $Payload "AstraRouter.exe") -Force
# Guard against the case-collision regression above: the staged app must never
# be the gateway binary under a different name.
$appHash = (Get-FileHash (Join-Path $Payload "AstraRouter.exe")).Hash
$gwHash = (Get-FileHash (Join-Path $pBin "astrarouter.exe")).Hash
if ($appHash -eq $gwHash) { throw "payload app and gateway are byte-identical; staging collision (see NOTE above)" }
# Next resolves static assets relative to server.js, so they live INSIDE it.
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
Remove-Item $NodeExe -ErrorAction SilentlyContinue
Remove-Item $unzip -Recurse -Force -ErrorAction SilentlyContinue

Step "setup-only installer exe"
$stamp = (Get-Date -Format o)
# NOTE: joined with "_" (no spaces) because a space inside the -X value breaks
# go's argv parsing when this script runs under Windows PowerShell 5.1, which
# does not re-quote embedded quotes for native commands (the hash would be
# treated as a package path: "package <hash> is not in std").
try { $stamp += "_" + (git -C $Repo rev-parse --short HEAD 2>$null) } catch {}
Push-Location $Desktop
try {
  $vetted = "github.com/shadowsafin/astrarouter/desktop/internal/payload.version=$stamp"
  & $Go build -trimpath -ldflags "-H windowsgui -X `"$vetted`"" -o (Join-Path $Dist "AstraRouterSetup.exe") ./cmd/installer
  if ($LASTEXITCODE -ne 0) { throw "installer build failed" }
} finally { Pop-Location }

Step "package extras"
Copy-Item (Join-Path $Desktop "README.md") (Join-Path $Dist "README.md") -Force
"desktop built $stamp" | Set-Content (Join-Path $Dist "VERSION.txt")
Remove-Item $Bin -Recurse -Force -ErrorAction SilentlyContinue

Write-Host "`nbuild ready: $Dist" -ForegroundColor Green
Get-ChildItem $Dist -File | ForEach-Object { Write-Host ("  {0}  {1:N1} MB" -f $_.Name, ($_.Length / 1MB)) }
