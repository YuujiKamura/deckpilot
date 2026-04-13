#!/bin/bash
# Deploy deckpilot.exe using Atomic Rename strategy.
#
# This allows deploying even if the binary is currently running, 
# because Windows allows renaming in-use files. The running process 
# will continue to use the renamed (.old) file until it exits.
#
# Usage:
#   scripts/deploy-to-ghostty.sh [path/to/deckpilot.exe]

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

SRC="${1:-$REPO_ROOT/deckpilot.exe}"
GHOSTTY_ROOT="${GHOSTTY_WIN_ROOT:-$HOME/ghostty-win}"

if [ ! -f "$SRC" ]; then
    echo "[deploy] source binary not found: $SRC" >&2
    exit 1
fi

# Define targets
TARGETS=(
    "$GHOSTTY_ROOT/zig-out-winui3/bin/deckpilot.exe"
    "$GHOSTTY_ROOT/zig-out-winui3-staging/bin/deckpilot.exe"
    "$HOME/bin/deckpilot.exe"
)

deployed=0
for dst in "${TARGETS[@]}"; do
    dst_dir="$(dirname "$dst")"
    
    # If ghostty-win subdir doesn't exist, skip it (maybe not built yet)
    if [[ "$dst" == *"$GHOSTTY_ROOT"* ]] && [ ! -d "$dst_dir" ]; then
        continue
    fi
    
    # Ensure dst_dir exists for non-ghostty targets (like $HOME/bin)
    mkdir -p "$dst_dir"

    # Atomic Rename:
    # 1. If destination exists, rename it to .old (allowed even if running)
    # 2. Copy new binary to destination
    if [ -f "$dst" ]; then
        dst_old="${dst}.old"
        rm -f "$dst_old" 2>/dev/null || true
        if ! mv -f "$dst" "$dst_old" 2>/dev/null; then
            echo "[deploy] could not rename $dst (skipping)" >&2
            continue
        fi
    fi

    if cp -f "$SRC" "$dst" 2>/dev/null; then
        echo "[deploy] OK: $dst"
        deployed=$((deployed + 1))
    else
        echo "[deploy] FAILED: $dst" >&2
    fi
done

echo "[deploy] finished. $deployed targets updated."
exit 0
