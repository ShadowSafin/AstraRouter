#!/usr/bin/env bash
# Native install helper (Linux/macOS): prerequisites, build, configure, migrate.
#
# This script is deliberately thin: every real step lives in
# `astrarouter native install`, so the scripted path and the manual path cannot
# drift apart. Run from the repository root:
#
#   bash install/native/install.sh
#
# Flags are passed through, e.g. `--skip-dashboard-build`, `--with-workers`.
# See `astrarouter native install --help` and documentation/installation/native.md.
set -euo pipefail

cd "$(dirname "$0")/../.."

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required tool: $1 ($2)" >&2
    exit 1
  fi
}

need go "install Go 1.27+ (https://go.dev/dl)"
if [ "${1:-}" != "--skip-dashboard-build" ] && [[ " $* " != *" --skip-dashboard "* ]]; then
  need node "install Node.js 20+ (https://nodejs.org)"
  need npm "install Node.js 20+ (https://nodejs.org)"
fi

if [ ! -f go.mod ]; then
  echo "run this script from the AstraRouter repository root" >&2
  exit 1
fi

# Build the installer binary first so the remaining steps run from a real
# install rather than `go run` (identical code, stable process identity for
# the supervisor's self-reference).
go build -trimpath -o bin/astrarouter ./cmd/astrarouter

./bin/astrarouter native install "$@"

echo
echo "Datastores still need to be reachable (PostgreSQL, Redis, ClickHouse and"
echo "NATS are host services in native mode — see the Datastores section of"
echo "documentation/installation/native.md), then:"
echo
echo "  ./bin/astrarouter native up"
