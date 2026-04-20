package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestProbeOnce_OSHungFastPath verifies the issue #26 pivot: when
// WatcherStatusOf returns "stalled" (the watcher's code word for
// IsHungAppWindow==TRUE), the action fires immediately without
// running the CPU sampler or the stall-seconds heuristic.
func TestProbeOnce_OSHungFastPath(t *testing.T) {
	var fired int32
	var seen HangInfo

	// Sampler would mark this session as "busy" (high CPU) so the
	// heuristic branch would NOT fire. Ensures the fast path is what
	// triggered the action.
	busy := &stubSampler{Percent: 90.0}
	// Fresh last-act — same point: heuristic would say "not stalled".
	fresh := func() time.Time { return time.Now() }

	cfg := makeConfig(func(c *HangConfig) {
		c.Sampler = busy
		c.LastActOf = fresh
		c.WatcherStatusOf = func() string { return "stalled" }
		c.Action = func(_ context.Context, info HangInfo) error {
			atomic.AddInt32(&fired, 1)
			seen = info
			return nil
		}
	})
	det, err := NewHangDetector(cfg)
	if err != nil {
		t.Fatalf("NewHangDetector: %v", err)
	}

	if err := det.probeOnce(context.Background()); err != nil {
		t.Fatalf("probeOnce: %v", err)
	}
	if atomic.LoadInt32(&fired) != 1 {
		t.Fatalf("expected fast-path action to fire exactly once, got %d", fired)
	}
	if seen.Source != "os-hung" {
		t.Errorf("HangInfo.Source=%q, want os-hung", seen.Source)
	}
	// Sampler must not have been invoked because the fast path
	// returned before reaching the heuristic branch.
	if busy.Calls != 0 {
		t.Errorf("Sampler was called %d times; fast path should have skipped it", busy.Calls)
	}
}

// TestProbeOnce_OSHungRespectsCooldown verifies that a "stalled"
// status within the cooldown window does not re-fire the action.
func TestProbeOnce_OSHungRespectsCooldown(t *testing.T) {
	var fired int32
	now := time.Now()
	clock := now

	cfg := makeConfig(func(c *HangConfig) {
		c.WatcherStatusOf = func() string { return "stalled" }
		c.Cooldown = 10 * time.Second
		c.Now = func() time.Time { return clock }
		c.Action = func(_ context.Context, _ HangInfo) error {
			atomic.AddInt32(&fired, 1)
			return nil
		}
	})
	det, _ := NewHangDetector(cfg)

	_ = det.probeOnce(context.Background())  // fires (1)
	_ = det.probeOnce(context.Background())  // inside cooldown, skip
	if atomic.LoadInt32(&fired) != 1 {
		t.Fatalf("cooldown violated: fired %d times, want 1", fired)
	}

	// Advance past cooldown → next probe fires again.
	clock = now.Add(15 * time.Second)
	_ = det.probeOnce(context.Background())
	if atomic.LoadInt32(&fired) != 2 {
		t.Fatalf("post-cooldown re-trigger failed: fired %d times, want 2", fired)
	}
}

// TestProbeOnce_OSHungFallsBackWhenCheckerNil verifies backward
// compatibility: when WatcherStatusOf is nil, the detector uses the
// heuristic branch as before.
func TestProbeOnce_OSHungFallsBackWhenCheckerNil(t *testing.T) {
	var fired int32
	idle := &stubSampler{Percent: 0.1} // below threshold
	stale := func() time.Time { return time.Now().Add(-10 * time.Minute) }

	cfg := makeConfig(func(c *HangConfig) {
		c.WatcherStatusOf = nil
		c.Sampler = idle
		c.LastActOf = stale
		c.Action = func(_ context.Context, info HangInfo) error {
			atomic.AddInt32(&fired, 1)
			if info.Source != "heuristic" {
				t.Errorf("expected heuristic source, got %q", info.Source)
			}
			return nil
		}
	})
	det, _ := NewHangDetector(cfg)
	_ = det.probeOnce(context.Background())
	if atomic.LoadInt32(&fired) != 1 {
		t.Errorf("heuristic branch failed to fire: %d", fired)
	}
}

// TestProbeOnce_OSHungIgnoresNonStalledStatus verifies the fast
// path only fires on "stalled" — "idle" / "active" / "" must not
// bypass the heuristic gate.
func TestProbeOnce_OSHungIgnoresNonStalledStatus(t *testing.T) {
	cases := []string{"", "idle", "active", "dead", "unknown"}
	for _, status := range cases {
		t.Run("status="+status, func(t *testing.T) {
			var fired int32
			busy := &stubSampler{Percent: 90.0}
			fresh := func() time.Time { return time.Now() }
			cfg := makeConfig(func(c *HangConfig) {
				c.WatcherStatusOf = func() string { return status }
				c.Sampler = busy
				c.LastActOf = fresh
				c.Action = func(_ context.Context, _ HangInfo) error {
					atomic.AddInt32(&fired, 1)
					return nil
				}
			})
			det, _ := NewHangDetector(cfg)
			_ = det.probeOnce(context.Background())
			if atomic.LoadInt32(&fired) != 0 {
				t.Errorf("status=%q triggered fast path; only 'stalled' should", status)
			}
		})
	}
}
