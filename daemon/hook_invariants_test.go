package daemon

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests lock invariants the current monolithic hook executor relies on.
// They exist so the planned hook-adapter refactor (#21) cannot ship without
// re-proving them. If they fail on current code, that's a pre-existing bug
// the refactor must not inherit.

// drainCaller consumes reqCh entries on the caller watcher and returns a
// function to stop the drain. Each drained request increments *got.
func drainCaller(w *Watcher, got *int32, payloads *[]string, mu *sync.Mutex) (stop func()) {
	stopCh := make(chan struct{})
	go func() {
		for {
			select {
			case req, ok := <-w.reqCh:
				if !ok {
					return
				}
				atomic.AddInt32(got, 1)
				if payloads != nil {
					mu.Lock()
					*payloads = append(*payloads, req.payload)
					mu.Unlock()
				}
				req.result <- pipeResult{content: "OK"}
			case <-stopCh:
				return
			}
		}
	}()
	return func() { close(stopCh) }
}

func newIdleNotification(target string) BufferNotification {
	return BufferNotification{
		SessionName:   target,
		Status:        "idle",
		StatusChanged: true,
		PrevStatus:    "active",
	}
}

// TestHookInvariant_OneTimeFiresExactlyOnce is the critical one for the
// adapter refactor. If per-adapter cleanup replaces the orchestrator's
// centralized removal, a second status transition within the cleanup window
// could double-fire the same one-time hook. Lock the invariant now.
func TestHookInvariant_OneTimeFiresExactlyOnce(t *testing.T) {
	d := New()
	callerW := NewWatcher("caller", "p", "f", 1, "", nil, nil)
	d.mu.Lock()
	d.watchers["caller"] = callerW
	d.mu.Unlock()

	d.AddIdleHook(IdleHook{
		Type:            "callback",
		SessionFilter:   "target",
		CallbackSession: "caller",
		OneTime:         true,
	})

	var got int32
	stop := drainCaller(callerW, &got, nil, nil)
	defer stop()

	notif := newIdleNotification("target")

	// Fire twice back-to-back. The cleanup goroutine sleeps 100ms before
	// removing the one-time hook, so any code path that rechecks
	// d.idleHooks within that window could re-enter.
	d.executeStatusHooks(notif)
	d.executeStatusHooks(notif)

	// Give both Execute goroutines + the 100ms cleanup time to drain.
	time.Sleep(300 * time.Millisecond)

	if n := atomic.LoadInt32(&got); n != 1 {
		t.Fatalf("OneTime invariant violated: hook fired %d times (expected 1)", n)
	}
	// And the hook must actually be gone from the registry afterwards.
	if hooks := d.ListIdleHooks(); len(hooks) != 0 {
		t.Fatalf("OneTime invariant violated: %d hook(s) still registered after fire", len(hooks))
	}
}

// TestHookInvariant_DeadCallbackSessionDoesNotCrash locks: when the callback
// session has been removed from the registry between hook registration and
// idle transition, executeCallbackHook must error cleanly, never panic.
// Regression guard for the adapter refactor risk of pre-capturing *Watcher.
func TestHookInvariant_DeadCallbackSessionDoesNotCrash(t *testing.T) {
	d := New()

	// Install, then immediately remove, a would-be caller. Hook references
	// it by name only — the name lookup at fire time must fail soft.
	tmpW := NewWatcher("ghost", "p", "f", 1, "", nil, nil)
	d.mu.Lock()
	d.watchers["ghost"] = tmpW
	d.mu.Unlock()

	d.AddIdleHook(IdleHook{
		Type:            "callback",
		SessionFilter:   "target",
		CallbackSession: "ghost",
		OneTime:         false,
	})

	d.mu.Lock()
	delete(d.watchers, "ghost")
	d.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("executeStatusHooks panicked with dead callback session: %v", r)
		}
	}()

	d.executeStatusHooks(newIdleNotification("target"))
	time.Sleep(50 * time.Millisecond) // let the Execute goroutine run
}

// TestHookInvariant_ConcurrentFiresDoNotRace spawns parallel status
// transitions to shake out data races in the hook executor. Run the suite
// with -race to catch regressions introduced by per-adapter state.
func TestHookInvariant_ConcurrentFiresDoNotRace(t *testing.T) {
	d := New()
	callerW := NewWatcher("caller", "p", "f", 1, "", nil, nil)
	d.mu.Lock()
	d.watchers["caller"] = callerW
	d.mu.Unlock()

	// Non-OneTime so we can fire many times without registration churn.
	d.AddIdleHook(IdleHook{
		Type:            "callback",
		SessionFilter:   "target",
		CallbackSession: "caller",
		OneTime:         false,
	})

	var got int32
	stop := drainCaller(callerW, &got, nil, nil)
	defer stop()

	const parallel = 16
	var wg sync.WaitGroup
	wg.Add(parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			defer wg.Done()
			d.executeStatusHooks(newIdleNotification("target"))
		}()
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	if n := atomic.LoadInt32(&got); n != parallel {
		t.Fatalf("concurrent invariant: expected %d fires, got %d", parallel, n)
	}
}

// TestHookInvariant_CallbackPayloadEndsWithNewline locks the \n-terminated
// wire contract with the ghostty input pipe. Without the newline the receiver
// sees the text queued in the prompt buffer but never submitted.
func TestHookInvariant_CallbackPayloadEndsWithNewline(t *testing.T) {
	d := New()
	callerW := NewWatcher("caller", "p", "f", 1, "", nil, nil)
	d.mu.Lock()
	d.watchers["caller"] = callerW
	d.mu.Unlock()

	d.AddIdleHook(IdleHook{
		Type:            "callback",
		SessionFilter:   "target",
		CallbackSession: "caller",
	})

	var got int32
	var payloads []string
	var pmu sync.Mutex
	stop := drainCaller(callerW, &got, &payloads, &pmu)
	defer stop()

	d.executeStatusHooks(newIdleNotification("target"))
	time.Sleep(100 * time.Millisecond)

	pmu.Lock()
	defer pmu.Unlock()
	if len(payloads) == 0 {
		t.Fatal("no callback payload captured")
	}
	for _, p := range payloads {
		if !strings.HasSuffix(p, "\n") {
			t.Fatalf("newline contract violated: payload %q must end with \\n", p)
		}
	}
}
