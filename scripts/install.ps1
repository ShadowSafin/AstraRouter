# AstraRouter single-command installer (Windows PowerShell).
#
#   irm https://raw.githubusercontent.com/shadowsafin/astrarouter/main/scripts/install.ps1 | iex
#
# Clones (or fast-forward updates) the repository into $env:ASTRAROUTER_DIR
# (default "$HOME\astrarouter") and hands off to scripts/deploy.ps1, which
# bootstraps .env, starts the full Docker Compose stack, waits for readiness
# and prints the URLs. Idempotent: re-running updates the checkout and
# re-verifies the stack without touching .env or volumes.
#
#   $env:ASTRAROUTER_DIR="$HOME\apps\astrarouter"; irm <url> | iex  # custom dir
#   $env:ASTRAROUTER_REF="main"        # branch/tag to check out (default: main)
#   $env:ASTRAROUTER_NO_DEPLOY="1"     # clone/update only, skip starting the stack
$ErrorActionPreference = "Stop"

$RepoUrl = "https://github.com/shadowsafin/astrarouter.git"
$Ref = if ($env:ASTRAROUTER_REF) { $env:ASTRAROUTER_REF } else { "main" }
$Target = if ($env:ASTRAROUTER_DIR) { $env:ASTRAROUTER_DIR } else { Join-Path $HOME "astrarouter" }

function Fail([string]$Step, [string]$Message) {
  Write-Host ""
  Write-Host "INSTALL FAILED during: $Step"
  Write-Host "  $Message"
  exit 1
}

Write-Host "AstraRouter installer: $RepoUrl @ $Ref -> $Target"

# 1. Prerequisites.
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
  Fail "prerequisite checks" "git not found. Install it from https://git-scm.com/downloads and re-run."
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  Fail "prerequisite checks" "docker not found. Install Docker Desktop from https://docs.docker.com/get-docker/ and re-run."
}
Write-Host "  OK  git + docker present (compose is checked by the deploy step)"

# 2. Fetch the repository.
if ((Test-Path -LiteralPath (Join-Path $Target ".git")) -and (Test-Path -LiteralPath $Target -PathType Container)) {
  Write-Host "  existing checkout found; fast-forwarding to latest ${Ref}..."
  & git -C $Target fetch --depth 1 origin $Ref
  if ($LASTEXITCODE -ne 0) { Fail "fetching the repository" "could not fetch from origin. Check network access to github.com." }
  & git -C $Target checkout -q $Ref
  if ($LASTEXITCODE -ne 0) { Fail "fetching the repository" "could not check out ${Ref}." }
  & git -C $Target reset -q --hard FETCH_HEAD
  if ($LASTEXITCODE -ne 0) { Fail "fetching the repository" "could not update the checkout." }
  Write-Host "  OK  checkout updated"
} elseif (Test-Path -LiteralPath $Target) {
  Fail "fetching the repository" "$Target exists but is not a git checkout. Move it aside or set `$env:ASTRAROUTER_DIR to another path."
} else {
  & git clone --depth 1 --branch $Ref $RepoUrl $Target
  if ($LASTEXITCODE -ne 0) { Fail "fetching the repository" "could not clone ${RepoUrl}. Check network access to github.com." }
  Write-Host "  OK  cloned into $Target"
}

$Deploy = Join-Path $Target "scripts\deploy.ps1"
if (-not (Test-Path -LiteralPath $Deploy)) {
  Fail "fetching the repository" "checkout at $Target has no scripts\deploy.ps1. Re-clone or pick another ASTRAROUTER_REF."
}

if ($env:ASTRAROUTER_NO_DEPLOY -eq "1") {
  Write-Host "  OK  checkout ready at $Target (stack start skipped by ASTRAROUTER_NO_DEPLOY=1)"
  Write-Host "  Start it later with: & `"$Deploy`""
  return
}

# 3. Hand off to the deploy script, which owns the rest of the run.
& $Deploy
