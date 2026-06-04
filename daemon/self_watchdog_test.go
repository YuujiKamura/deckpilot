package daemon

import (
	"sync/atomic"
	"testing"
	"time"
)

// makeFakeExit returns an exit func that records the first exit code on a
// buffered channel (extra calls are dropped, so the watchdog's post-exit
// return path cannot panic or block) and the channel to observe it.
func makeFakeExit() (func(int), <-chan int) {
	exited := make(chan int, 1)
	return func(code int) {
		select {
		case exited <- code:
		default:
		}
	}, exited
}

// TestSelfWatchdog_NoExitWhenTicking verifies that while discovery keeps
// ticking, the watchdog never exits the process. A helper goroutine refreshes
// lastDiscoveryTick faster than the timeout; the watchdog must stay quiet.
func TestSelfWatchdog_NoExitWhenTicking(t *testing.T) {
	d := New()
	fakeExit, exited := makeFakeExit()
	stop := make(chan struct{})
	defer close(stop)

	interval := 20 * time.Millisecond
	timeout := 60 * time.Millisecond

	// Keep ticking well within the timeout.
	go func() {
		t := time.NewTicker(interval / 2)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				d.markDiscoveryTick()
			}
		}
	}()

	go d.selfWatchdog(interval, timeout, fakeExit, stop)

	select {
	case code := <-exited:
		t.Fatalf("watchdog exited (code=%d) while discovery was still ticking", code)
	case <-time.After(300 * time.Millisecond):
		// No exit within ~15 intervals: correct.
	}
}

// TestSelfWatchdog_ExitsWhenStale verifies that after a single seed and then
// no further ticks, the watchdog exits with code 1 once the timeout elapses.
func TestSelfWatchdog_ExitsWhenStale(t *testing.T) {
	d := New()
	fakeExit, exited := makeFakeExit()
	stop := make(chan struct{})
	defer close(stop)

	d.markDiscoveryTick() // seed once, then go stale

	interval := 20 * time.Millisecond
	timeout := 60 * time.Millisecond
	go d.selfWatchdog(interval, timeout, fakeExit, stop)

	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("watchdog exit code=%d, want 1", code)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not exit despite stale discovery tick")
	}
}

// TestSelfWatchdog_ExitsWhenNeverTicked verifies that a lastDiscoveryTick of 0
// (discovery never seeded/ran) is treated as the deeper bug it is and exits.
func TestSelfWatchdog_ExitsWhenNeverTicked(t *testing.T) {
	d := New()
	// Do NOT seed: lastDiscoveryTick stays 0 (Unix epoch when read).
	fakeExit, exited := makeFakeExit()
	stop := make(chan struct{})
	defer close(stop)

	interval := 20 * time.Millisecond
	timeout := 60 * time.Millisecond
	go d.selfWatchdog(interval, timeout, fakeExit, stop)

	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("watchdog exit code=%d, want 1", code)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not exit when discovery never ticked")
	}
}

// TestMarkDiscoveryTick_AdvancesField verifies the helper writes a fresh
// monotonic-ish timestamp into lastDiscoveryTick, within the call window, so
// the watchdog's liveness read is sourced correctly.
func TestMarkDiscoveryTick_AdvancesField(t *testing.T) {
	d := New()
	if got := atomic.LoadInt64(&d.lastDiscoveryTick); got != 0 {
		t.Fatalf("fresh daemon lastDiscoveryTick=%d, want 0", got)
	}

	before := time.Now().UnixNano()
	d.markDiscoveryTick()
	after := time.Now().UnixNano()

	got := atomic.LoadInt64(&d.lastDiscoveryTick)
	if got < before || got > after {
		t.Fatalf("lastDiscoveryTick=%d outside call window [%d, %d]", got, before, after)
	}
}
