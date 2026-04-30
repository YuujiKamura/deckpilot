package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LaunchMeta records the parameters `deckpilot launch` used to spawn a
// session. It is written to ~/.deckpilot/launch-meta/<session>.json
// once waitForNewSession returns the new session name, and read back
// by the hang-detect snapshot action so a hung session's dump carries
// an exact `deckpilot launch ...` resume command instead of leaving
// the operator to reconstruct cwd / agent from the buffer tail.
//
// Storage is file-based on purpose: it survives daemon restarts (the
// daemon's session map is in-memory) and avoids widening the IPC
// surface. A missing file is treated as "session was not started by
// deckpilot launch" — snapshot v2 falls back to the inferred agent.
type LaunchMeta struct {
	SessionName string    `json:"session_name"`
	Agent       string    `json:"agent"`
	Cwd         string    `json:"cwd"`
	Prompt      string    `json:"prompt"`
	LaunchedAt  time.Time `json:"launched_at"`
}

// launchMetaTimeNow is overridable so tests get deterministic timestamps.
var launchMetaTimeNow = time.Now

// launchMetaUserHome mirrors the seam used elsewhere in cmd/ so tests
// can redirect HOME without touching the process env.
var launchMetaUserHome = os.UserHomeDir

func launchMetaDir() string {
	home, err := launchMetaUserHome()
	if err == nil {
		return filepath.Join(home, ".deckpilot", "launch-meta")
	}
	return filepath.Join(os.TempDir(), "deckpilot-launch-meta")
}

func launchMetaPath(sessionName string) string {
	return filepath.Join(launchMetaDir(), sanitizeForFilenameCmd(sessionName)+".json")
}

// WriteLaunchMeta persists meta for sessionName. Returns an error so
// the caller can surface partial-failure (the launch itself already
// succeeded; failing to record metadata is a degraded outcome, not a
// fatal one — the caller decides whether to abort).
func WriteLaunchMeta(meta LaunchMeta) error {
	if meta.LaunchedAt.IsZero() {
		meta.LaunchedAt = launchMetaTimeNow()
	}
	dir := launchMetaDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("launch-meta mkdir: %w", err)
	}
	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("launch-meta marshal: %w", err)
	}
	path := launchMetaPath(meta.SessionName)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("launch-meta write %s: %w", path, err)
	}
	return nil
}

// oneLineForHeader collapses an arbitrary prompt into a single
// header-safe line: newlines become spaces, runs of whitespace
// collapse, and the result is truncated so a multi-line prompt does
// not break the snapshot's `# key: value` parser-friendly format.
func oneLineForHeader(s string) string {
	const max = 200
	out := make([]byte, 0, len(s))
	prevSpace := false
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			if !prevSpace {
				out = append(out, ' ')
				prevSpace = true
			}
		case ' ':
			if !prevSpace {
				out = append(out, ' ')
				prevSpace = true
			}
		default:
			out = append(out, string(r)...)
			prevSpace = false
		}
	}
	trimmed := string(out)
	for len(trimmed) > 0 && trimmed[0] == ' ' {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == ' ' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) > max {
		trimmed = trimmed[:max] + "..."
	}
	return trimmed
}

// shellQuote returns a double-quoted form safe to paste into a Windows
// cmd or POSIX shell: embedded double-quotes are escaped. Used by the
// snapshot's resume_command line so cwd / prompt with spaces survive
// copy-paste. Not a full shell-quoting library, just enough for the
// resume hint.
func shellQuote(s string) string {
	if s == "" {
		return `""`
	}
	needsQuote := false
	for _, r := range s {
		if r == ' ' || r == '"' || r == '\t' || r == '\\' || r == '$' || r == '`' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return s
	}
	escaped := make([]byte, 0, len(s)+2)
	escaped = append(escaped, '"')
	for _, r := range s {
		if r == '"' {
			escaped = append(escaped, '\\', '"')
			continue
		}
		escaped = append(escaped, string(r)...)
	}
	escaped = append(escaped, '"')
	return string(escaped)
}

// ReadLaunchMeta loads previously-recorded meta. Returns ok=false (and
// no error) when the file simply does not exist — that is the common
// case for sessions started outside deckpilot. Genuine I/O or parse
// errors are returned.
func ReadLaunchMeta(sessionName string) (LaunchMeta, bool, error) {
	path := launchMetaPath(sessionName)
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LaunchMeta{}, false, nil
		}
		return LaunchMeta{}, false, fmt.Errorf("launch-meta read: %w", err)
	}
	var meta LaunchMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return LaunchMeta{}, false, fmt.Errorf("launch-meta parse %s: %w", path, err)
	}
	return meta, true, nil
}
