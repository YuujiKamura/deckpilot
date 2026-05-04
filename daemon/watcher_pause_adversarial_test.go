package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestPausePolling_NegativeTimeoutIsImmediate: a negative duration is
// nonsensical input; treat as zero-timeout (non-blocking probe) rather
// than blocking forever or panicking on time.NewTimer(d<0).
func TestPausePolling_NegativeTimeoutIsImmediate(t *testing.T) {
	w := NewWatcher("test-neg", "", "", 0, "0", nil)
	start := time.Now()
	_, err := w.PausePolling(-5 * time.Second)
	elapsed := time.Since(start)
	if err == nil {
		t.Error("PausePolling(-5s) with no runner should fail")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("PausePolling(-5s) took %v — should be effectively immediate", elapsed)
	}
}

// TestPausePolling_ConcurrentCallersDoNotDeadlockEachOther: when N
// goroutines call PausePolling concurrently against a runner that is
// alive, the first one wins the unbuffered send and the rest must
// time out cleanly without blocking goroutines or panicking. This is
// the regression fixture for the "multiple handleSend in flight against
// the same session" scenario — without timeout, late callers would hang
// indefinitely waiting for the runner that is blocked on the first
// caller's resume.
func TestPausePolling_ConcurrentCallersDoNotDeadlockEachOther(t *testing.T) {
	w := NewWatcher("test-concurrent", "", "", 0, "0", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	const N = 5
	var wg sync.WaitGroup
	results := make([]error, N)
	resumeChs := make([]chan struct{}, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rc, err := w.PausePolling(500 * time.Millisecond)
			results[i] = err
			resumeChs[i] = rc
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent PausePolling callers did not all complete within 3s — possible deadlock")
	}

	// Cleanup any successful resumeChs and assert exactly one succeeded
	// (the runner can only hand out one pause at a time).
	successCount := 0
	for i, err := range results {
		if err == nil {
			successCount++
			if resumeChs[i] != nil {
				close(resumeChs[i])
			}
		} else if !errors.Is(err, ErrPausePollingTimeout) {
			t.Errorf("caller %d got unexpected error: %v", i, err)
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 successful PausePolling among %d concurrent callers, got %d",
			N, successCount)
	}
}

// TestPausePolling_NilResumeOnError: when PausePolling errors, the
// returned channel must be nil so callers cannot accidentally close(nil)
// or send on a half-baked channel.
func TestPausePolling_NilResumeOnError(t *testing.T) {
	w := NewWatcher("test-nil", "", "", 0, "0", nil)
	rc, err := w.PausePolling(50 * time.Millisecond)
	if err == nil {
		t.Skip("PausePolling unexpectedly succeeded; skipping nil-channel check")
	}
	if rc != nil {
		t.Errorf("PausePolling errored but returned non-nil channel %v — caller could close(nil)", rc)
	}
}
