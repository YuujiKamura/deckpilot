package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/YuujiKamura/deckpilot/pipe"
)

// ActionNotify is the default, non-destructive hang response. It writes
// a structured log line to stderr (via log.Printf) and — when the
// Daemon reference is non-nil — fires any registered idle hook tagged
// for the session so external monitors can react. Never touches the
// agent process or its PTY.
//
// The returned HangAction closes over d so callers can pre-wire the
// daemon reference at config time without creating circular packages.
func ActionNotify(d *Daemon) HangAction {
	return func(_ context.Context, info HangInfo) error {
		log.Printf("hang-notify[%s]: pid=%d procs=%d cpu=%.2f%% stall=%s",
			info.SessionName, info.RootPID, info.ProcessCount,
			info.CPUPercent, info.StallSince)
		fmt.Fprintf(os.Stderr,
			"[hang] %s: pid=%d cpu=%.2f%% stall=%s — consider `deckpilot send %s $'\\x03'` or escalate\n",
			info.SessionName, info.RootPID, info.CPUPercent, info.StallSince, info.SessionName)
		// Optional: fire idle hooks targeted at this session so a
		// monitoring session receives the event push.
		if d != nil {
			d.fireHangHook(info)
		}
		return nil
	}
}

// ActionSendCtrlC sends a single 0x03 byte to the session's pipe. If
// the agent's TUI handles SIGINT (most do), this cancels the current
// activity without killing the process. If the TUI swallows it, this
// is a no-op — escalation should move to ActionKillChild or higher.
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
		if err := pipe.SendRaw(pipePath, []byte{0x03}); err != nil {
			return fmt.Errorf("ActionSendCtrlC: send: %w", err)
		}
		log.Printf("hang-ctrlc[%s]: 0x03 sent to %s", info.SessionName, pipePath)
		return nil
	}
}

// ActionKillChild stops the deepest descendant process that looks like
// an agent's workload child (not Ghostty itself), leaving the session
// alive for inspection. Selection heuristic:
//   - Walk the process tree rooted at info.RootPID
//   - Exclude any process whose name contains "ghostty" (case-insensitive)
//   - Among the remaining, pick the one with the oldest start order
//     (earliest in BFS order after root's children) — typically the
//     test binary or agent subprocess
//
// If no candidate exists, returns an error so the caller can escalate
// to ActionKillSession.
func ActionKillChild(lister ProcessLister) HangAction {
	return func(_ context.Context, info HangInfo) error {
		tree, err := GetProcessTree(lister, info.RootPID)
		if err != nil {
			return fmt.Errorf("ActionKillChild: tree: %w", err)
		}
		var target *ProcessInfo
		for i := range tree {
			p := &tree[i]
			if p.PID == info.RootPID {
				continue
			}
			if strings.Contains(strings.ToLower(p.Name), "ghostty") {
				continue
			}
			// First eligible wins (BFS order → earliest descendant).
			target = p
			break
		}
		if target == nil {
			return fmt.Errorf("ActionKillChild: no non-ghostty descendant of %d", info.RootPID)
		}
		if err := killProcessFn(target.PID); err != nil {
			return fmt.Errorf("ActionKillChild: kill %d (%s): %w",
				target.PID, target.Name, err)
		}
		log.Printf("hang-killchild[%s]: killed pid=%d name=%s",
			info.SessionName, target.PID, target.Name)
		return nil
	}
}

// ActionKillSession terminates the Ghostty process itself. Last-resort
// remediation: the user will see the window close and any work inside
// the session is lost. Should be opt-in only.
func ActionKillSession() HangAction {
	return func(_ context.Context, info HangInfo) error {
		if err := killProcessFn(info.RootPID); err != nil {
			return fmt.Errorf("ActionKillSession: kill %d: %w", info.RootPID, err)
		}
		log.Printf("hang-killsession[%s]: killed root pid=%d",
			info.SessionName, info.RootPID)
		return nil
	}
}

// fireHangHook is a stub that would dispatch idle-hook notifications
// for a hang event. Implemented as a no-op here; the integration point
// with the real idle-hook registry lives in daemon.go / types.go. Kept
// as a method so tests can assert it was invoked without spinning up a
// full daemon.
func (d *Daemon) fireHangHook(info HangInfo) {
	// Real implementation would match info.SessionName against
	// d.idleHooks and fire any matching send-hook with a synthesized
	// event message. Left as a hook point so the hang detector can
	// ship without touching the idle-hook protocol — see issue #26
	// M4 for the wire-up.
	_ = info
}

// killPID is the platform-agnostic entry point; implementations live
// in hangdetect_actions_windows.go (os.FindProcess + Process.Kill) and
// a future hangdetect_actions_unix.go (SIGKILL).
//
// killProcessFn is the package-level hook the actions call. Tests
// substitute a fake that records PIDs without actually killing.
var killProcessFn = killPID
