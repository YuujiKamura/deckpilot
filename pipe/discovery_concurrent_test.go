//go:build windows

package pipe

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestProbePIDsConcurrent_RunsInParallel pins down the fix for the
// "list: no response from daemon" symptom seen on 2026-04-26: a serial
// loop over hung ghostty PIDs blew past the daemon's 10s IPC handler
// deadline. Each fake probe sleeps 200ms, so 5 PIDs serially would
// take ~1s. The concurrent implementation must finish well under that.
func TestProbePIDsConcurrent_RunsInParallel(t *testing.T) {
	const perProbe = 200 * time.Millisecond
	pids := []int{10, 20, 30, 40, 50}

	probe := func(pid int) (Session, bool) {
		time.Sleep(perProbe)
		return Session{Name: fmt.Sprintf("ghostty-%d", pid), PID: pid}, true
	}

	start := time.Now()
	got := probePIDsConcurrent(pids, probe)
	elapsed := time.Since(start)

	// Serial would be 5 × 200ms = 1000ms. Parallel should be ~200ms
	// plus scheduling slop. Use 600ms ceiling: well below serial,
	// well above any plausible parallel run.
	if elapsed >= 600*time.Millisecond {
		t.Fatalf("probePIDsConcurrent serialized: elapsed=%v want <600ms (serial would be ~%v)",
			elapsed, time.Duration(len(pids))*perProbe)
	}

	if len(got) != len(pids) {
		t.Fatalf("got %d sessions, want %d", len(got), len(pids))
	}
	// Order must match input order so callers (e.g. handleList) see
	// deterministic results regardless of probe completion timing.
	for i, s := range got {
		if s.PID != pids[i] {
			t.Errorf("result[%d].PID=%d, want %d (order not preserved)", i, s.PID, pids[i])
		}
	}
}

// TestProbePIDsConcurrent_DropsFailures ensures probes that return
// ok=false (the worker behavior when both winui3 and win32 pipes fail
// to Ping) are filtered out — a hung ghostty whose pipe is dead must
// not appear in pipe.Discover's output.
func TestProbePIDsConcurrent_DropsFailures(t *testing.T) {
	pids := []int{1, 2, 3, 4}
	probe := func(pid int) (Session, bool) {
		// Drop even PIDs.
		if pid%2 == 0 {
			return Session{}, false
		}
		return Session{PID: pid}, true
	}
	got := probePIDsConcurrent(pids, probe)
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
	if got[0].PID != 1 || got[1].PID != 3 {
		t.Errorf("unexpected order/contents: %+v", got)
	}
}

// TestProbePIDsConcurrent_EmptyInput is a degenerate-case guard so we
// don't allocate a wg or spin a goroutine pool when there are no
// ghostty processes — this path is taken every refreshSessions cycle.
func TestProbePIDsConcurrent_EmptyInput(t *testing.T) {
	if got := probePIDsConcurrent(nil, nil); got != nil {
		t.Errorf("expected nil for empty input, got %+v", got)
	}
}

// TestProbePIDsConcurrent_OneSlowProbeDoesNotStallOthers regression-
// guards the specific shape of the 2026-04-26 outage: a single hung
// ghostty making Ping take its full 7s budget must not block other
// healthy sessions from being reported.
func TestProbePIDsConcurrent_OneSlowProbeDoesNotStallOthers(t *testing.T) {
	pids := []int{1, 2, 3}
	var fastFinishedAt atomic.Int64

	probe := func(pid int) (Session, bool) {
		if pid == 2 {
			time.Sleep(500 * time.Millisecond)
		} else {
			fastFinishedAt.Store(time.Now().UnixNano())
		}
		return Session{PID: pid}, true
	}

	start := time.Now()
	probePIDsConcurrent(pids, probe)

	fastDone := time.Unix(0, fastFinishedAt.Load())
	if fastDone.Sub(start) > 200*time.Millisecond {
		t.Errorf("fast probe was blocked by slow probe: took %v from start",
			fastDone.Sub(start))
	}
}
