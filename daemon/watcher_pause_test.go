package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPausePolling_ReturnsResumeChannelWhenRunnerListens: happy path —
// Run() goroutine is selecting on pauseCh, PausePolling completes quickly
// and returns a resumeCh that, when closed, lets Run() resume polling.
func TestPausePolling_ReturnsResumeChannelWhenRunnerListens(t *testing.T) {
	w := NewWatcher("test-listens", "", "", 0, "0", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Give Run() a moment to enter its select.
	time.Sleep(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		resumeCh, err := w.PausePolling(2 * time.Second)
		if err == nil {
			close(resumeCh)
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("PausePolling with live runner should succeed, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PausePolling hung even though runner was alive")
	}
}

// TestPausePolling_TimesOutWhenRunnerAbsent: this is the regression
// fixture for the unbuffered-channel-send hazard. If the Run() goroutine
// has exited (dead session) or has not been started, PausePolling must
// not block forever — it must time out and return an error so the caller
// (handleSend) can fall through gracefully.
func TestPausePolling_TimesOutWhenRunnerAbsent(t *testing.T) {
	// No Run() goroutine started — pauseCh has no receiver.
	w := NewWatcher("test-no-runner", "", "", 0, "0", nil)

	start := time.Now()
	resumeCh, err := w.PausePolling(200 * time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Errorf("PausePolling should error when no runner is listening; got resumeCh=%v", resumeCh)
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("PausePolling returned too quickly (%v) — timeout not honoured", elapsed)
	}
	if elapsed > 1*time.Second {
		t.Errorf("PausePolling exceeded its timeout window: %v", elapsed)
	}
	if !errors.Is(err, ErrPausePollingTimeout) {
		t.Errorf("expected ErrPausePollingTimeout, got %v", err)
	}
}

// TestPausePolling_ZeroTimeoutFailsImmediately: a zero timeout is a
// non-blocking probe — useful for callers that want "pause if free, else
// proceed without pause" semantics.
func TestPausePolling_ZeroTimeoutFailsImmediately(t *testing.T) {
	w := NewWatcher("test-zero", "", "", 0, "0", nil)
	start := time.Now()
	_, err := w.PausePolling(0)
	if err == nil {
		t.Error("PausePolling(0) with no runner should fail immediately")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("PausePolling(0) should be near-instant, took %v", elapsed)
	}
}
