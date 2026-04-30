#!/bin/bash
# Deploy deckpilot.exe to local ghostty-win checkout.
# No-op if ghostty-win is not present locally.
#
# Usage:
#   scripts/deploy-to-ghostty.sh [path/to/deckpilot.exe]
#
# Env:
#   GHOSTTY_WIN_ROOT         Override ghostty-win location (default: $HOME/ghostty-win)
#   DECKPILOT_DEPLOY=0       Skip deployment entirely
#   DECKPILOT_KILL_STALE=0   Skip stale daemon detection/kill

set -u

if [ "${DECKPILOT_DEPLOY:-1}" = "0" ]; then
    exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

SRC="${1:-$REPO_ROOT/deckpilot.exe}"
GHOSTTY_ROOT="${GHOSTTY_WIN_ROOT:-$HOME/ghostty-win}"

# No ghostty-win locally -> silent no-op
if [ ! -d "$GHOSTTY_ROOT" ]; then
    exit 0
fi

if [ ! -f "$SRC" ]; then
    echo "[deploy-to-ghostty] source binary not found: $SRC" >&2
    exit 0
fi

# Deploy targets (only to build output dirs that already exist)
TARGETS=(
    "$GHOSTTY_ROOT/zig-out-winui3/bin/deckpilot.exe"
    "$GHOSTTY_ROOT/zig-out-win32/bin/deckpilot.exe"
)

deployed=0
for dst in "${TARGETS[@]}"; do
    dst_dir="$(dirname "$dst")"
    [ -d "$dst_dir" ] || continue
    if cp -f "$SRC" "$dst" 2>/dev/null; then
        echo "[deploy-to-ghostty] $dst"
        deployed=$((deployed + 1))
    else
        echo "[deploy-to-ghostty] failed: $dst" >&2
    fi
done

if [ "$deployed" = 0 ]; then
    # ghostty-win exists but no build output dirs -> still a no-op, just inform
    echo "[deploy-to-ghostty] no zig-out-*/bin/ dirs in $GHOSTTY_ROOT (build ghostty first)"
fi

# ---------------------------------------------------------------------------
# Stale daemon detection and kill
# ---------------------------------------------------------------------------
# Skip if disabled via env
if [ "${DECKPILOT_KILL_STALE:-1}" = "0" ]; then
    exit 0
fi

# Collect candidate paths: all deployed targets + any deckpilot in PATH
CANDIDATE_DIRS=()
for dst in "${TARGETS[@]}"; do
    CANDIDATE_DIRS+=("$(dirname "$dst")")
done
# Also include the repo root (where SRC lives)
CANDIDATE_DIRS+=("$REPO_ROOT")

# Get new binary mtime (seconds since epoch) using PowerShell for portability
# stat --printf='%Y' works in Git Bash but use PowerShell as a safer fallback
_get_mtime() {
    local path="$1"
    # Try GNU stat first (available in Git for Windows)
    if stat --printf='%Y' "$path" 2>/dev/null; then
        return
    fi
    # Fallback: PowerShell
    powershell.exe -NoProfile -Command \
        "[int][double]::Parse((Get-Item '$path').LastWriteTime.ToUniversalTime().Subtract([datetime]'1970-01-01').TotalSeconds)" \
        2>/dev/null
}

# Determine reference mtime: the newest deployed file (or SRC itself)
REF_MTIME=0
for dst in "${TARGETS[@]}"; do
    [ -f "$dst" ] || continue
    m=$(_get_mtime "$dst")
    [ -n "$m" ] || continue
    if [ "$m" -gt "$REF_MTIME" ] 2>/dev/null; then
        REF_MTIME="$m"
    fi
done
# If nothing deployed, use SRC mtime
if [ "$REF_MTIME" = "0" ] && [ -f "$SRC" ]; then
    REF_MTIME=$(_get_mtime "$SRC")
fi

if [ -z "$REF_MTIME" ] || [ "$REF_MTIME" = "0" ]; then
    echo "[deploy-to-ghostty] could not determine binary mtime, skipping stale-daemon check" >&2
    exit 0
fi

# Query running deckpilot processes via PowerShell
# Returns lines of: <PID> <ExePath>
PROC_LIST=$(powershell.exe -NoProfile -Command '
    Get-Process -Name deckpilot -ErrorAction SilentlyContinue |
    Select-Object -Property Id,Path |
    ForEach-Object {
        if ($_.Path) { "$($_.Id) $($_.Path)" }
    }
' 2>/dev/null) || true

if [ -z "$PROC_LIST" ]; then
    exit 0
fi

    # Convert a Unix-style path (Git Bash /c/foo) to Windows style (C:\foo)
    # so we can compare with PowerShell output which always uses Windows paths.
    _to_winpath() {
        local p="$1"
        # /<drive>/path -> <Drive>:/path  then flip slashes
        echo "$p" | sed 's|^/\([a-zA-Z]\)/|\1:/|' | sed 's|/|\\|g'
    }

echo "$PROC_LIST" | while IFS=' ' read -r pid exe_path; do
    [ -n "$pid" ] || continue
    [ -n "$exe_path" ] || continue

    # Safety: never kill ghostty.exe
    exe_basename=$(echo "$exe_path" | sed 's|.*[/\\]||' | tr '[:upper:]' '[:lower:]')
    if [ "$exe_basename" != "deckpilot.exe" ]; then
        continue
    fi

    # Normalize: convert backslash to forward slash, lowercase for case-insensitive compare
    exe_norm=$(echo "$exe_path" | sed 's|\\|/|g' | tr '[:upper:]' '[:lower:]')

    # Check if this process belongs to a known candidate dir
    is_ours=0
    for cdir in "${CANDIDATE_DIRS[@]}"; do
        # Convert Git Bash path to Windows-style then normalize to forward slashes
        cdir_win=$(_to_winpath "$cdir")
        cdir_norm=$(echo "$cdir_win" | sed 's|\\|/|g' | tr '[:upper:]' '[:lower:]')
        # Prefix match (exe lives inside this dir)
        case "$exe_norm" in
            "$cdir_norm"/*) is_ours=1; break ;;
        esac
    done

    if [ "$is_ours" = "0" ]; then
        continue
    fi

    # Get process start time as epoch via PowerShell
    proc_start=$(powershell.exe -NoProfile -Command "
        \$p = Get-Process -Id $pid -ErrorAction SilentlyContinue
        if (\$p) {
            [int][double]::Parse(\$p.StartTime.ToUniversalTime().Subtract([datetime]'1970-01-01').TotalSeconds)
        }
    " 2>/dev/null) || true

    if [ -z "$proc_start" ]; then
        continue
    fi

    # mtime diff check: if process started < binary mtime AND diff > 2s -> stale
    diff=$(( REF_MTIME - proc_start ))
    if [ "$diff" -le 2 ] 2>/dev/null; then
        # Process started after (or within 2s of) new binary -> not stale
        continue
    fi

    if [ "$proc_start" -lt "$REF_MTIME" ] 2>/dev/null; then
        echo "[deploy-to-ghostty] stale daemon detected: PID=$pid path=$exe_path (started=${proc_start}, binary_mtime=${REF_MTIME}, diff=${diff}s)"
        if taskkill //F //PID "$pid" 2>/dev/null; then
            echo "[deploy-to-ghostty] killed PID=$pid"
        else
            echo "[deploy-to-ghostty] failed to kill PID=$pid" >&2
        fi
    fi
done

exit 0
