package daemon

import (
	"testing"
	"time"
)

// These tests exercise the pure policy layer (policy.go) without constructing
// any *Daemon, *Watcher, pipe, or goroutine. That is the whole point of the
// refactor: the policy that decides "who should receive an idle callback" is
// now testable in isolation.

func TestResolveCaller_ExactMatchWinsOverHeuristics(t *testing.T) {
	now := time.Now()
	snaps := []SessionSnapshot{
		{Name: "session-a", Status: "active", LastPollOK: now},
		{Name: "session-b", Status: "idle", LastPollOK: now.Add(-time.Second)},
	}

	if got := ResolveCaller("session-b", snaps); got != "session-b" {
		t.Fatalf("exact match: want session-b, got %q", got)
	}
}

func TestResolveCaller_EmptyInputReturnsEmpty(t *testing.T) {
	if got := ResolveCaller("", []SessionSnapshot{{Name: "a", Status: "active"}}); got != "" {
		t.Fatalf("empty caller: want \"\", got %q", got)
	}
}

func TestResolveCaller_FallsBackToActiveSession(t *testing.T) {
	snaps := []SessionSnapshot{
		{Name: "idle-old", Status: "idle", LastPollOK: time.Now().Add(-time.Minute)},
		{Name: "active-one", Status: "active", LastPollOK: time.Now()},
	}

	if got := ResolveCaller("wt:unknown-guid", snaps); got != "active-one" {
		t.Fatalf("active fallback: want active-one, got %q", got)
	}
}

func TestResolveCaller_FallsBackToMostRecentWhenNoneActive(t *testing.T) {
	now := time.Now()
	snaps := []SessionSnapshot{
		{Name: "old", Status: "idle", LastPollOK: now.Add(-time.Minute)},
		{Name: "recent", Status: "idle", LastPollOK: now},
	}

	if got := ResolveCaller("pid:9999", snaps); got != "recent" {
		t.Fatalf("recency fallback: want recent, got %q", got)
	}
}

func TestResolveCaller_ReturnsEmptyWhenNoSnapshots(t *testing.T) {
	if got := ResolveCaller("wt:anything", nil); got != "" {
		t.Fatalf("no snapshots: want \"\", got %q", got)
	}
}

func TestShouldRegisterCallback_RejectsSameRaw(t *testing.T) {
	snaps := []SessionSnapshot{{Name: "a", Status: "active"}}
	d := ShouldRegisterCallback("a", "a", snaps)
	if d.Register || d.Reason != CallbackRejectSameRaw {
		t.Fatalf("same_raw: got %+v", d)
	}
}

func TestShouldRegisterCallback_RejectsUnresolvable(t *testing.T) {
	// No snapshots -> nothing to fall back onto -> unresolved.
	d := ShouldRegisterCallback("target", "wt:unknown", nil)
	if d.Register || d.Reason != CallbackRejectUnresolved {
		t.Fatalf("unresolved: got %+v", d)
	}
}

// TestShouldRegisterCallback_RejectsHeuristicSelfLoop_PureLayer is the
// pure-policy counterpart of the integration repro in callback_test.go. It
// proves the self-loop guard is enforced without touching any *Daemon or
// *Watcher — which is the whole reason for extracting policy.go.
func TestShouldRegisterCallback_RejectsHeuristicSelfLoop_PureLayer(t *testing.T) {
	// Only one watcher, and it's active because it just received a send.
	// An external caller (no matching watcher) will heuristically resolve
	// to this same session; the guard must reject.
	snaps := []SessionSnapshot{
		{Name: "ghostty-71964", Status: "active", LastPollOK: time.Now()},
	}

	d := ShouldRegisterCallback("ghostty-71964", "wt:external-claude-code-guid", snaps)
	if d.Register {
		t.Fatalf("self-loop accepted: %+v (issue #19 regression)", d)
	}
	if d.Reason != CallbackRejectHeuristicLoop {
		t.Fatalf("wrong reject reason: want %q, got %q", CallbackRejectHeuristicLoop, d.Reason)
	}
	if d.ActualCaller != "ghostty-71964" {
		t.Fatalf("actual caller should be reported for diagnostics, got %q", d.ActualCaller)
	}
}

func TestShouldRegisterCallback_RejectsMissingTarget(t *testing.T) {
	snaps := []SessionSnapshot{
		{Name: "caller-session", Status: "idle", LastPollOK: time.Now()},
	}

	d := ShouldRegisterCallback("nonexistent", "caller-session", snaps)
	if d.Register || d.Reason != CallbackRejectTargetNotFound {
		t.Fatalf("target_not_found: got %+v", d)
	}
}

func TestShouldRegisterCallback_AcceptsDistinctAgents(t *testing.T) {
	snaps := []SessionSnapshot{
		{Name: "agent-a", Status: "idle", LastPollOK: time.Now().Add(-time.Second)},
		{Name: "agent-b", Status: "active", LastPollOK: time.Now()},
	}

	d := ShouldRegisterCallback("agent-b", "agent-a", snaps)
	if !d.Register {
		t.Fatalf("distinct agents rejected: %+v", d)
	}
	if d.ActualCaller != "agent-a" {
		t.Fatalf("wrong resolved caller: got %q", d.ActualCaller)
	}
	if d.Reason != CallbackAccept {
		t.Fatalf("wrong accept reason: got %q", d.Reason)
	}
}
