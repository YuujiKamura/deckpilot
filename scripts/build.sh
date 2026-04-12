#!/bin/bash
# Build deckpilot.exe with ldflags (Version, Commit, BuildTime).
# After a successful build, calls scripts/deploy-to-ghostty.sh.
#
# Usage:
#   bash scripts/build.sh
#
# Env:
#   DECKPILOT_SKIP_DEPLOY=1   Build only; skip deploy step

set -e
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$REPO_ROOT"

VERSION="$(git describe --always --tags --dirty 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "[build] Version=$VERSION Commit=${COMMIT:0:12} BuildTime=$BUILD_TIME"

go build \
    -ldflags "-X main.Version=$VERSION -X main.Commit=$COMMIT -X main.BuildTime=$BUILD_TIME" \
    -o deckpilot.exe \
    main.go

echo "[build] deckpilot.exe built successfully"

if [ "${DECKPILOT_SKIP_DEPLOY:-0}" = "1" ]; then
    exit 0
fi

exec bash "$SCRIPT_DIR/deploy-to-ghostty.sh" "$REPO_ROOT/deckpilot.exe"
