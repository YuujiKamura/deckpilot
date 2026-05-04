package cmd

import (
	"os"
	"path/filepath"
	"time"
)

// Centralized seams for filesystem layout, clock, and filename
// sanitization shared by hangdetect / cleanup / launchmeta.
//
// Tests redirect via deckpilotUserHome / deckpilotNow rather than
// owning per-feature copies (hangDetectUserHome, launchMetaUserHome,
// cleanupNow, etc. — those used to live one per file and drifted out
// of sync).
var (
	deckpilotUserHome = os.UserHomeDir
	deckpilotNow      = time.Now
)

// deckpilotHomeDir returns the per-user state root. Falls back to
// %TEMP%/deckpilot when $HOME / %USERPROFILE% is not resolvable, so
// tooling never blows up on weird shells.
func deckpilotHomeDir() string {
	home, err := deckpilotUserHome()
	if err == nil {
		return filepath.Join(home, ".deckpilot")
	}
	return filepath.Join(os.TempDir(), "deckpilot")
}

// hangDumpsDir is where hang-detect snapshot files live and what
// `deckpilot cleanup` sweeps under the hang-dumps label.
func hangDumpsDir() string { return filepath.Join(deckpilotHomeDir(), "hang-dumps") }

// launchMetaDir is where `deckpilot launch` records per-session
// resume metadata (agent / cwd / prompt / launched_at) and what
// `deckpilot cleanup` sweeps under the launch-meta label.
func launchMetaDir() string { return filepath.Join(deckpilotHomeDir(), "launch-meta") }

// managedWorktreesDir is where `deckpilot launch` creates detached
// per-session git worktrees for capability isolation.
func managedWorktreesDir() string { return filepath.Join(deckpilotHomeDir(), "worktrees") }

// idleHooksDir is where `deckpilot notify add` persists user-configured
// idle hooks so they survive daemon restarts (issue #31). NOT swept by
// `deckpilot cleanup` — these are user configuration, not artefacts:
// neither age-based GC (operator may reasonably leave a hook in place
// for weeks) nor liveness GC (no bound session) is appropriate. The
// daemon owns add/remove via IPC; this is the cmd-side view.
//
// Note: daemon/idle_hooks_persist.go has its own copy of this path
// resolution to avoid a daemon→cmd import cycle (cmd already imports
// daemon for IPC helpers). Keep the two implementations aligned.
func idleHooksDir() string { return filepath.Join(deckpilotHomeDir(), "idle-hooks") }

// sanitizeFilename replaces characters that Windows / POSIX
// filesystems reject in path segments. Also collapses to "unnamed"
// when the input would yield an empty filename.
func sanitizeFilename(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\x00':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "unnamed"
	}
	return string(out)
}
