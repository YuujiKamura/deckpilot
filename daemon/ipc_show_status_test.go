package daemon

import (
	"errors"
	"testing"
)

// TestComposeShowStatus_FreshSuccessReturnsWatcherStatus: when FreshTail
// returns no error, the watcher status is the source of truth and is
// returned unmodified.
func TestComposeShowStatus_FreshSuccessReturnsWatcherStatus(t *testing.T) {
	cases := []struct {
		watcherStatus string
		want          string
	}{
		{"active", "active"},
		{"idle", "idle"},
		{"busy", "busy"},
		{"stalled", "stalled"},
	}
	for _, c := range cases {
		got := composeShowStatus(nil, c.watcherStatus)
		if got != c.want {
			t.Errorf("composeShowStatus(nil, %q) = %q, want %q", c.watcherStatus, got, c.want)
		}
	}
}

// TestComposeShowStatus_FreshFailureMarksStale: when FreshTail errors and
// the caller is about to fall back to LastContent, the response must
// signal "stale:<watcherStatus>" so the client knows the buffer is cached.
//
// This is the regression fixture for the 2026-05-04 incident where
// handleShow silently returned LastContent on BUSY|renderer_locked,
// leaving the user staring at a frozen screen with no indication that
// the live tail had failed.
func TestComposeShowStatus_FreshFailureMarksStale(t *testing.T) {
	cases := []struct {
		err           error
		watcherStatus string
		want          string
	}{
		{errors.New("server: BUSY|renderer_locked"), "active", "stale:active"},
		{errors.New("server: BUSY|data_lane_full"), "busy", "stale:busy"},
		{errors.New("server: TIMEOUT|ui_thread_busy"), "idle", "stale:idle"},
		{errors.New("dial pipe: file not found"), "active", "stale:active"},
		{errors.New("server: NO_TABS"), "dead", "stale:dead"},
	}
	for _, c := range cases {
		got := composeShowStatus(c.err, c.watcherStatus)
		if got != c.want {
			t.Errorf("composeShowStatus(%v, %q) = %q, want %q",
				c.err, c.watcherStatus, got, c.want)
		}
	}
}

// TestComposeShowStatus_EmptyWatcherStatusFallsBackToUnknown: defensive —
// if Status() returns empty string we still emit something parseable.
func TestComposeShowStatus_EmptyWatcherStatusFallsBackToUnknown(t *testing.T) {
	if got := composeShowStatus(nil, ""); got != "unknown" {
		t.Errorf("composeShowStatus(nil, \"\") = %q, want %q", got, "unknown")
	}
	if got := composeShowStatus(errors.New("x"), ""); got != "stale:unknown" {
		t.Errorf("composeShowStatus(err, \"\") = %q, want %q", got, "stale:unknown")
	}
}
