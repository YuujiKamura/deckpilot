package cmd

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeVerifier is a scripted ApprovalVerifier for testing
// waitForApprovalResolved in isolation from the daemon.
type fakeVerifier struct {
	// sequence of (content, err) buffer reads, consumed in order. The
	// last entry repeats once sequence is exhausted.
	frames []frame
	calls  int32
}

type frame struct {
	content string
	err     error
}

func (f *fakeVerifier) BufferOf(session, caller string) (string, error) {
	n := atomic.AddInt32(&f.calls, 1)
	idx := int(n - 1)
	if idx >= len(f.frames) {
		idx = len(f.frames) - 1
	}
	return f.frames[idx].content, f.frames[idx].err
}

// HasPrompt reuses the production matcher so the fake exercises the real
// DetectApprovalPromptForAgent logic.
func (f *fakeVerifier) HasPrompt(content, agent string) bool {
	_, ok := DetectApprovalPromptForAgent(content, agent)
	return ok
}

// TestWaitForApprovalResolved_ImmediatelyResolved verifies the fast-path
// when the very first poll sees the modal already gone.
func TestWaitForApprovalResolved_ImmediatelyResolved(t *testing.T) {
	fv := &fakeVerifier{frames: []frame{{content: "no modal here", err: nil}}}
	start := time.Now()
	ok := waitForApprovalResolved(fv, "s", "c", "claude",
		2*time.Second, 100*time.Millisecond)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("expected resolved=true on empty buffer")
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("fast path took %v, expected <150ms", elapsed)
	}
	if atomic.LoadInt32(&fv.calls) != 1 {
		t.Errorf("expected 1 buffer read, got %d", fv.calls)
	}
}

// TestWaitForApprovalResolved_ResolvesMidFlight verifies the common case:
// first few polls still see the modal, a later poll sees it cleared.
func TestWaitForApprovalResolved_ResolvesMidFlight(t *testing.T) {
	// claude patterns include "Action Required"
	withPrompt := "Action Required on your approval"
	fv := &fakeVerifier{
		frames: []frame{
			{content: withPrompt},
			{content: withPrompt},
			{content: "all clear"},
		},
	}
	ok := waitForApprovalResolved(fv, "s", "c", "claude",
		1*time.Second, 50*time.Millisecond)

	if !ok {
		t.Fatal("expected resolved=true after modal cleared")
	}
	if calls := atomic.LoadInt32(&fv.calls); calls < 3 {
		t.Errorf("expected ≥3 buffer reads, got %d", calls)
	}
}

// TestWaitForApprovalResolved_Timeout verifies that a never-resolving
// prompt returns false after the deadline without overrunning.
func TestWaitForApprovalResolved_Timeout(t *testing.T) {
	withPrompt := "Action Required — stuck forever"
	fv := &fakeVerifier{frames: []frame{{content: withPrompt}}}

	start := time.Now()
	ok := waitForApprovalResolved(fv, "s", "c", "claude",
		300*time.Millisecond, 50*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Error("expected resolved=false when prompt persists")
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("returned too early at %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("overran deadline: %v", elapsed)
	}
}

// TestWaitForApprovalResolved_BufferErrorsIgnored verifies that a run of
// BufferOf errors does not falsely resolve; the function keeps polling.
func TestWaitForApprovalResolved_BufferErrorsIgnored(t *testing.T) {
	bufErr := errors.New("daemon unreachable")
	fv := &fakeVerifier{
		frames: []frame{
			{err: bufErr},
			{err: bufErr},
			{content: "cleared"},
		},
	}
	ok := waitForApprovalResolved(fv, "s", "c", "claude",
		500*time.Millisecond, 30*time.Millisecond)

	if !ok {
		t.Fatal("expected eventual resolved=true past error frames")
	}
	if calls := atomic.LoadInt32(&fv.calls); calls < 3 {
		t.Errorf("expected ≥3 reads (errors should not short-circuit), got %d", calls)
	}
}

// TestWaitForApprovalResolved_NegativePollInterval verifies that a
// non-positive pollInterval is replaced by a safe default instead of
// causing a tight CPU loop.
func TestWaitForApprovalResolved_NegativePollInterval(t *testing.T) {
	fv := &fakeVerifier{frames: []frame{{content: "Action Required"}}}
	start := time.Now()
	ok := waitForApprovalResolved(fv, "s", "c", "claude",
		400*time.Millisecond, 0) // 0 → default
	elapsed := time.Since(start)

	if ok {
		t.Error("expected resolved=false with persistent prompt")
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("returned too early: %v — default poll may be missing", elapsed)
	}
	// Default is 250ms; we should have taken at most ~600ms (default poll
	// + one final check).
	if elapsed > 1*time.Second {
		t.Errorf("default poll took too long: %v", elapsed)
	}
}

// TestWaitForApprovalResolved_ZeroTimeoutStillPollsOnce verifies that a
// zero timeout performs at least one buffer read (no-poll = no info).
func TestWaitForApprovalResolved_ZeroTimeoutStillPollsOnce(t *testing.T) {
	fv := &fakeVerifier{frames: []frame{{content: "all clear"}}}
	ok := waitForApprovalResolved(fv, "s", "c", "claude", 0, 100*time.Millisecond)
	if !ok {
		t.Fatal("zero timeout + clear buffer should still return true")
	}
	if atomic.LoadInt32(&fv.calls) != 1 {
		t.Errorf("expected exactly 1 read on zero timeout, got %d", fv.calls)
	}
}

// TestDaemonVerifier_HasPromptDelegates ensures the production verifier's
// HasPrompt passes through to DetectApprovalPromptForAgent — guards
// against a future refactor silently breaking the production path.
func TestDaemonVerifier_HasPromptDelegates(t *testing.T) {
	v := daemonVerifier{}
	if !v.HasPrompt("Action Required now", "claude") {
		t.Error("expected HasPrompt to match claude pattern")
	}
	if v.HasPrompt("benign output only", "claude") {
		t.Error("expected HasPrompt to return false for benign text")
	}
	// Unknown agent falls back to claude patterns per
	// DetectApprovalPromptForAgent contract.
	if !v.HasPrompt("Action Required", "unknown-xyz") {
		t.Error("expected unknown-agent fallback to claude patterns")
	}
}
