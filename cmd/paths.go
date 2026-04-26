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
