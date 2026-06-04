package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// submitFailureMarkers are the daemon error substrings that mean "the Enter
// keystroke did not submit the typed text" — the text is still sitting in the
// TUI input box. These are recoverable by nudging Enter again. A broken pipe
// or a missing session produces a different error message and is NOT in this
// list, because retrying those cannot help.
//
// Note: daemon.DaemonSend surfaces submit_failed_stuck as a non-nil error (it
// returns "ERR|submit_failed_stuck|<n>ms", and DaemonSend turns any "ERR|"
// response into an error — see daemon/ipc.go), NOT as a result string. The
// classification therefore inspects err.Error(), not the result.
var submitFailureMarkers = []string{"submit_failed_stuck", "submit_unconfirmed"}

// isSubmitFailure reports whether err is a recoverable submit failure (Enter
// did not take) as opposed to a pipe/session error that retrying cannot fix.
func isSubmitFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, m := range submitFailureMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// sendFunc sends msg to a session and returns the daemon's raw result string
// plus an error. An empty msg means "press Enter only" (the daemon's
// empty-message path sends a bare \r). Production wires this to
// daemon.DaemonSend bound to a fixed session/caller; tests pass a stub.
type sendFunc func(msg string) (string, error)

// readHashFunc returns a hash of the session's current TUI buffer. Production
// wires this to DaemonShow(...,"buffer",...) + daemon.BufHash; tests pass a
// stub. It is used to detect whether an Enter nudge finally submitted the
// stuck text (the buffer moves) versus stayed stuck (the buffer is unchanged).
type readHashFunc func() (string, error)

// sendWithSubmitRetry sends msg once. On success it returns immediately. On a
// recoverable submit failure (Enter did not take; the typed text is still in
// the input box per daemon.SubmitFailedStuck) it nudges Enter up to `retries`
// times with `backoff` between attempts, checking after each nudge whether the
// buffer moved past the stuck snapshot — movement proves the submit finally
// fired. A non-submit error (pipe / missing session) is returned without
// retry.
//
// Re-typing msg is deliberately NOT done: on a genuine stuck the text is still
// in the box, so a re-type would double it. Nudging Enter alone is safe in
// both the genuine-stuck case (submits the in-box text) and the false-positive
// case (a no-op on an already-submitted box).
func sendWithSubmitRetry(send sendFunc, readHash readHashFunc, msg string, retries int, backoff time.Duration) (string, error) {
	result, err := send(msg)
	if err == nil {
		return result, nil
	}
	if !isSubmitFailure(err) {
		return result, err // pipe / session error: retrying won't help
	}

	// Capture the stuck buffer so a later Enter nudge that finally submits
	// (buffer moves) can be distinguished from one that stays stuck (buffer
	// unchanged). An empty stuckHash (read failed) disables the movement
	// check, so we fall through to the failure return rather than risk a
	// false "recovered".
	stuckHash, _ := readHash()

	for attempt := 1; attempt <= retries; attempt++ {
		if _, nudgeErr := send(""); nudgeErr != nil {
			return "", nudgeErr // pipe broke mid-retry; nothing left to do
		}
		time.Sleep(backoff) // let the TUI redraw if the Enter took
		h, readErr := readHash()
		if readErr == nil && stuckHash != "" && h != stuckHash {
			return fmt.Sprintf("OK|submit_recovered|enter_nudge_%d", attempt), nil
		}
	}
	return "", fmt.Errorf("submit failed after %d enter-nudge retries: %w", retries, err)
}

// daemonSendWithRetry is the production wrapper around sendWithSubmitRetry,
// binding the injectable send/readHash to the real daemon for a fixed
// session+caller.
func daemonSendWithRetry(sess, msg, caller string, retries int, backoff time.Duration) (string, error) {
	send := func(m string) (string, error) {
		return daemon.DaemonSend(sess, m, caller)
	}
	readHash := func() (string, error) {
		buf, _, err := daemon.DaemonShow(sess, "buffer", caller)
		if err != nil {
			return "", err
		}
		return daemon.BufHash(buf), nil
	}
	return sendWithSubmitRetry(send, readHash, msg, retries, backoff)
}

// killGhosttyOnLeak kills a leaked Ghostty process when a launch fails after
// the window was already started. Safe to call with pid <= 0 (no-op). Errors
// are logged to stderr but never returned: the caller is already on the exit
// path, and a failed kill cannot be recovered there.
//
// os.FindProcess(pid).Kill() is the cross-platform primitive (TerminateProcess
// on Windows, signal on Unix). PID reuse between launch and this kill is
// theoretically possible but negligible in the seconds-long window for a
// class (c) dev tool.
func killGhosttyOnLeak(pid int) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "killGhosttyOnLeak: find pid=%d: %v\n", pid, err)
		return
	}
	if err := p.Kill(); err != nil {
		fmt.Fprintf(os.Stderr, "killGhosttyOnLeak: kill pid=%d: %v\n", pid, err)
	}
}
