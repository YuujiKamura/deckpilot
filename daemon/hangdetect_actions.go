// Package daemon — hang response actions (issue #26).
//
// Design philosophy (per issue #26 addendum, 2026-04-19):
//
//   A hang detector that *kills* hung processes is the wrong default.
//   Silent termination destroys the agent's reasoning chain, the
//   queued input buffer, and any post-mortem evidence. That makes
//   the tool strictly worse than leaving the process alone, because
//   at least an idle process can still be inspected.
//
//   Every action in this file is therefore NON-DESTRUCTIVE:
//     - ActionNotify:   log + external hook fire (no process change)
//     - ActionSnapshot: capture PTY tail to disk before anything
//                       else can destroy it (preservation)
//     - ActionSendCtrlC: the single exception — a *soft* interrupt
//                       that the agent TUI can catch and recover
//                       from, preserving state where possible
//
//   There is intentionally NO kill-child / kill-session action in
//   this codebase. If an operator genuinely needs to terminate a
//   hung Ghostty, `Stop-Process` is one shell invocation away —
//   automating that loses more than it gains.
//
//   Tier hierarchy (CLI wires these in order):
//     Lv1  notify    — log + supervisor ping
//     Lv2  snapshot  — preserve current buffer to disk
//     Lv3  soft-int  — Ctrl+C via PTY; agent may recover on its own
//     Lv4  escalate  — human decides; no automatic action
package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

// ActionNotify is the Lv1, non-destructive hang response. It writes
// a structured log line to stderr (via log.Printf) and — when the
// Daemon reference is non-nil — fires any registered idle hook tagged
// for the session so external monitors can react. Never touches the
// agent process or its PTY.
func ActionNotify(d *Daemon) HangAction {
	return func(_ context.Context, info HangInfo) error {
		log.Printf("hang-notify[%s]: pid=%d procs=%d cpu=%.2f%% stall=%s",
			info.SessionName, info.RootPID, info.ProcessCount,
			info.CPUPercent, info.StallSince)
		fmt.Fprintf(os.Stderr,
			"[hang] %s: pid=%d cpu=%.2f%% stall=%s — consider snapshot or manual inspection\n",
			info.SessionName, info.RootPID, info.CPUPercent, info.StallSince)
		if d != nil {
			d.fireHangHook(info)
		}
		return nil
	}
}

// ActionSnapshot is the Lv2 preservation response. Before anything
// destructive can happen (manually or by a later tier), dump the
// last N lines of the session's PTY to disk so the operator has an
// evidence record of what the agent was doing at the moment of the
// hang. Writes to <baseDir>/<YYYYMMDD-HHMMSS>-<session>.log.
//
// baseDir is typically "<home>/.deckpilot/hang-dumps"; callers that
// want a different location can pass any absolute path. An empty
// baseDir falls back to the default.
//
// tailLines controls how much buffer history to preserve (0 → 200).
// The tail is pulled via pipe.Tail; if the session has no pipe path
// (web session), the dump records "no pipe available" instead of
// silently succeeding with an empty file.
func ActionSnapshot(d *Daemon, baseDir string, tailLines int) HangAction {
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			baseDir = filepath.Join(home, ".deckpilot", "hang-dumps")
		} else {
			baseDir = filepath.Join(os.TempDir(), "deckpilot-hang-dumps")
		}
	}
	if tailLines <= 0 {
		tailLines = 200
	}

	return func(_ context.Context, info HangInfo) error {
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			return fmt.Errorf("ActionSnapshot: mkdir %s: %w", baseDir, err)
		}

		ts := info.DetectedAt
		if ts.IsZero() {
			ts = time.Now()
		}
		filename := fmt.Sprintf("%s-%s.log",
			ts.Format("20060102-150405"),
			sanitizeForFilename(info.SessionName))
		path := filepath.Join(baseDir, filename)

		header := fmt.Sprintf(
			"# deckpilot hang snapshot\n"+
				"# session: %s\n"+
				"# root_pid: %d\n"+
				"# process_count: %d\n"+
				"# cpu_percent: %.2f\n"+
				"# stall_since: %s\n"+
				"# detected_at: %s\n"+
				"# --- buffer tail (%d lines) ---\n",
			info.SessionName, info.RootPID, info.ProcessCount,
			info.CPUPercent, info.StallSince,
			ts.Format(time.RFC3339), tailLines)

		body := ""
		if d != nil {
			pipePath, ok := d.resolvePipePath(info.SessionName)
			if ok && pipePath != "" {
				tail, err := pipe.Tail(pipePath, tailLines)
				if err != nil {
					body = fmt.Sprintf("[snapshot error: tail failed: %v]\n", err)
				} else {
					body = tail
				}
			} else {
				body = "[snapshot note: no pipe path for this session (web session?)]\n"
			}
		} else {
			body = "[snapshot note: daemon reference is nil; cannot resolve pipe]\n"
		}

		if err := os.WriteFile(path, []byte(header+body), 0o644); err != nil {
			return fmt.Errorf("ActionSnapshot: write %s: %w", path, err)
		}
		log.Printf("hang-snapshot[%s]: saved %s", info.SessionName, path)
		return nil
	}
}

// ActionSendCtrlC is the Lv3 soft-interrupt. It sends a single 0x03
// byte to the session's pipe. If the agent's TUI handles SIGINT (most
// do — Gemini, Codex, Claude all implement it), this cancels the
// current activity without killing the process, and the agent can
// typically recover and accept new input. If the TUI swallows it, no
// further automatic action is taken — escalation is the operator's
// call.
//
// Requires pipePath lookup via the Daemon; if the session is web-only
// (no pipe) this returns an error so the caller can escalate.
func ActionSendCtrlC(d *Daemon) HangAction {
	return func(_ context.Context, info HangInfo) error {
		if d == nil {
			return fmt.Errorf("ActionSendCtrlC: nil daemon")
		}
		pipePath, ok := d.resolvePipePath(info.SessionName)
		if !ok || pipePath == "" {
			return fmt.Errorf("ActionSendCtrlC: no pipe for session %q", info.SessionName)
		}
		if _, err := pipe.SendRaw(pipePath, []byte{0x03}); err != nil {
			return fmt.Errorf("ActionSendCtrlC: send: %w", err)
		}
		log.Printf("hang-ctrlc[%s]: 0x03 sent to %s", info.SessionName, pipePath)
		return nil
	}
}

// ChainActions returns a HangAction that runs each action in sequence.
// If one returns an error, it is logged and the next still runs —
// preservation (snapshot) must not be blocked by a failing notify, and
// a failing soft-interrupt must not prevent the snapshot from being
// captured. The aggregate error is the first non-nil result (for
// observability), or nil when all succeed.
func ChainActions(actions ...HangAction) HangAction {
	return func(ctx context.Context, info HangInfo) error {
		var firstErr error
		for i, a := range actions {
			if a == nil {
				continue
			}
			if err := a(ctx, info); err != nil {
				log.Printf("hang-chain[%s]: action #%d error: %v",
					info.SessionName, i, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		return firstErr
	}
}

// fireHangHook is a stub that would dispatch idle-hook notifications
// for a hang event. Left as a method so tests can assert it was
// invoked; the real wire-up to the idle-hook registry is tracked in
// issue #26 M4 (observability).
func (d *Daemon) fireHangHook(info HangInfo) {
	_ = info
}

// sanitizeForFilename replaces path separators and other characters
// that cause trouble on Windows file systems with an underscore.
func sanitizeForFilename(s string) string {
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
