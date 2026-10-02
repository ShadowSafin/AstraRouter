#!/usr/bin/env bash
# Synapass single-pipe installer.
#
#   curl -fsSL https://raw.githubusercontent.com/shadowsafin/synapass/main/scripts/install.sh | bash
#
# Clones (or fast-forward updates) the repository into $SYNAPASS_DIR
# (default ~/synapass) and hands off to scripts/deploy.sh, which bootstraps
# .env, starts the full Docker Compose stack, waits for readiness and prints
# the URLs. Idempotent: re-running updates the checkout and re-verifies the
# stack without touching .env or volumes.
#
# Piped-stdin safe: this script never reads from stdin (bash itself owns stdin
# when piped), and every child command that might is given </dev/null.
#
#   SYNAPASS_DIR=~/apps/synapass curl -fsSL <url> | bash   # custom dir
#   SYNAPASS_REF=main        # branch/tag to check out (default: main)
#   SYNAPASS_NO_DEPLOY=1     # clone/update only, skip starting the stack
set -euo pipefail

REPO_URL="https://github.com/shadowsafin/synapass.git"
REF="${SYNAPASS_REF:-main}"
TARGET="${SYNAPASS_DIR:-$HOME/synapass}"
STEP="starting up"

info()    { printf '  %s\n' "$*"; }
success() { printf '  OK  %s\n' "$*"; }
fail() {
  printf '\nINSTALL FAILED during: %s\n' "$STEP"
  printf '  %s\n' "$*"
  exit 1
}
trap 'fail "unexpected error (exit $?)."' ERR

need_cmd() { # $1=cmd [$2=hint]
  command -v "$1" >/dev/null 2>&1 || fail "missing required tool: $1. ${2:-Install it and re-run.}"
}

echo "Synapass installer: ${REPO_URL} @ ${REF} -> ${TARGET}"

STEP="prerequisite checks"
need_cmd git "Install git (https://git-scm.com/downloads) and re-run."
need_cmd docker "Install Docker Desktop (https://docs.docker.com/get-docker/) and re-run."
success "git + docker present (compose is checked by the deploy step)"

STEP="fetching the repository"
if [ -d "$TARGET/.git" ]; then
  info "existing checkout found; fast-forwarding to latest ${REF}..."
  git -C "$TARGET" fetch --depth 1 origin "$REF" </dev/null || fail "could not fetch from origin. Check network access to github.com."
  git -C "$TARGET" checkout -q "$REF" </dev/null || fail "could not check out ${REF}."
  git -C "$TARGET" reset -q --hard "FETCH_HEAD" </dev/null || fail "could not update the checkout."
  success "checkout updated"
elif [ -e "$TARGET" ]; then
  fail "$TARGET exists but is not a git checkout. Move it aside or set SYNAPASS_DIR to another path."
else
  git clone --depth 1 --branch "$REF" "$REPO_URL" "$TARGET" </dev/null \
    || fail "could not clone ${REPO_URL}. Check network access to github.com."
  success "cloned into $TARGET"
fi

DEPLOY="$TARGET/scripts/deploy.sh"
[ -f "$DEPLOY" ] || fail "checkout at $TARGET has no scripts/deploy.sh. Re-clone or pick another SYNAPASS_REF."

if [ "${SYNAPASS_NO_DEPLOY:-0}" = "1" ]; then
  success "checkout ready at $TARGET (stack start skipped by SYNAPASS_NO_DEPLOY=1)"
  info "Start it later with: bash \"$DEPLOY\""
  exit 0
fi

STEP="handing off to the deploy script"
# Replace this process with deploy.sh: one log stream, and deploy's own
# set -e / ERR trap governs the rest of the run.
exec bash "$DEPLOY"
