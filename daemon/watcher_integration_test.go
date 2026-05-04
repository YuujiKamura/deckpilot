package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeClient satisfies pipeClient for watcher integration tests.
// tailFn and pingFn are injectable; all other methods are no-ops.
type fakeClient struct {
	tailFn func(lines int) (string, error)
	pingFn func() error
}

func (f *fakeClient) Tail(lines int) (string, error) {
	if f.tailFn != nil {
		return f.tailFn(lines)
	}
	return "line1\nline2", nil
}
func (f *fakeClient) Ping() error {
	if f.pingFn != nil {
		return f.pingFn()
	}
	return nil
}
func (f *fakeClient) SendKeys(text string) (uint32, error)  { return 0, nil }
func (f *fakeClient) SendRaw(data []byte) (uint32, error)   { return 0, nil }
func (f *fakeClient) WaitForAck(cmdID uint32) error         { return nil }
func (f *fakeClient) History() (string, error)              { return "", nil }
func (f *fakeClient) Close() error                          { return nil }

// TestWatcherPoll_BusyTransient: poll() on a BUSY error must set status="busy"
// and NOT advance deadRetries — backpressure is transient.
func TestWatcherPoll_BusyTransient(t *testing.T) {
	fc := &fakeClient{
		tailFn: func(lines int) (string, error) {
			return "", fmt.Errorf("BUSY|renderer_locked")
		},
	}
	w := newWatcherForTest("busy-session", fc)
	w.poll()

	if got := w.Status(); got != "busy" {
		t.Errorf("Status after BUSY error = %q, want %q", got, "busy")
	}
	w.mu.Lock()
	retries := w.deadRetries
	w.mu.Unlock()
	if retries != 0 {
		t.Errorf("deadRetries after BUSY = %d, want 0 (must not erode budget)", retries)
	}
}

// TestWatcherPoll_BusyWrapped: BUSY signal wrapped in the standard
// `server: ERR|BUSY|...` envelope must still be classified as transient.
func TestWatcherPoll_BusyWrapped(t *testing.T) {
	fc := &fakeClient{
		tailFn: func(lines int) (string, error) {
			return "", errors.New("server: ERR|BUSY|data_lane_full")
		},
	}
	w := newWatcherForTest("busy-wrapped", fc)
	w.poll()

	if got := w.Status(); got != "busy" {
		t.Errorf("Status after wrapped BUSY = %q, want %q", got, "busy")
	}
	w.mu.Lock()
	retries := w.deadRetries
	w.mu.Unlock()
	if retries != 0 {
		t.Errorf("deadRetries after wrapped BUSY = %d, want 0", retries)
	}
}

// TestWatcherPoll_NoTabsMarksDead: NO_TABS is session-fatal; poll() must mark
// the watcher dead immediately without waiting for 3 retries.
func TestWatcherPoll_NoTabsMarksDead(t *testing.T) {
	fc := &fakeClient{
		tailFn: func(lines int) (string, error) {
			return "", errors.New("NO_TABS")
		},
	}
	w := newWatcherForTest("notabs-session", fc)
	w.poll()

	if got := w.Status(); got != "dead" {
		t.Errorf("Status after NO_TABS = %q, want %q", got, "dead")
	}
	w.mu.Lock()
	retries := w.deadRetries
	w.mu.Unlock()
	// NO_TABS bypasses retry budget entirely; counter must remain 0.
	if retries != 0 {
		t.Errorf("deadRetries after NO_TABS = %d, want 0 (bypass path)", retries)
	}
}

// TestWatcherPoll_NoTabsSubstringNotFatal: "NO_TABS" appearing as a substring
// inside a longer diagnostic (e.g. "NO_TABS_ALLOWED") must NOT mark dead.
func TestWatcherPoll_NoTabsSubstringNotFatal(t *testing.T) {
	fc := &fakeClient{
		tailFn: func(lines int) (string, error) {
			return "", errors.New("diagnostic: NO_TABS_ALLOWED in this build")
		},
	}
	w := newWatcherForTest("notabs-substr", fc)
	w.poll()

	if got := w.Status(); got == "dead" {
		t.Errorf("NO_TABS as substring must not mark dead; got %q", got)
	}
}

// TestWatcherPoll_Transport3xMarksDead: three consecutive transport-level
// errors (non-BUSY, non-NO_TABS) must promote the session to dead.
func TestWatcherPoll_Transport3xMarksDead(t *testing.T) {
	fc := &fakeClient{
		tailFn: func(lines int) (string, error) {
			return "", errors.New("transport: broken pipe")
		},
	}
	w := newWatcherForTest("transport-session", fc)

	for i := range 2 {
		w.poll()
		if got := w.Status(); got == "dead" {
			t.Errorf("poll %d: premature dead; should need 3 retries", i+1)
		}
	}
	w.poll() // 3rd failure → dead
	if got := w.Status(); got != "dead" {
		t.Errorf("Status after 3 transport errors = %q, want %q", got, "dead")
	}
}

// TestWatcherPoll_TransportRecovery: a successful poll after transport errors
// must reset deadRetries so the budget is not permanently eroded.
func TestWatcherPoll_TransportRecovery(t *testing.T) {
	calls := 0
	fc := &fakeClient{
		tailFn: func(lines int) (string, error) {
			calls++
			if calls <= 2 {
				return "", errors.New("transport: temporary")
			}
			return "recovered content", nil
		},
	}
	w := newWatcherForTest("recovery-session", fc)
	w.poll() // fail 1
	w.poll() // fail 2
	w.poll() // success → resets counter

	w.mu.Lock()
	retries := w.deadRetries
	w.mu.Unlock()
	if retries != 0 {
		t.Errorf("deadRetries after recovery = %d, want 0", retries)
	}
	if got := w.Status(); got == "dead" {
		t.Errorf("should have recovered; got status %q", got)
	}
}

// TestWatcherFreshTail_StaleMarkerPath: when FreshTail fails, the caller
// (handleShow) should receive a non-nil error. composeShowStatus then adds
// the "stale:" prefix so the client can distinguish live vs cached content.
// This test exercises the full path: fake client → FreshTail via Run goroutine
// → error propagation → composeShowStatus "stale:" prefix.
func TestWatcherFreshTail_StaleMarkerPath(t *testing.T) {
	fc := &fakeClient{
		tailFn: func(lines int) (string, error) {
			return "", errors.New("transport: no pipe")
		},
	}
	w := newWatcherForTest("stale-session", fc)
	go w.Run(t.Context())
	time.Sleep(30 * time.Millisecond) // let goroutine start

	_, freshErr := w.FreshTail(50)
	if freshErr == nil {
		t.Fatal("FreshTail should have returned an error for stale path")
	}
	status := composeShowStatus(freshErr, w.Status())
	if !strings.HasPrefix(status, "stale:") {
		t.Errorf("composeShowStatus with freshErr = %q, want stale: prefix", status)
	}
}

// TestWatcherPausePolling_TimeoutFallThrough: when no Run goroutine is
// listening, PausePolling must return ErrPausePollingTimeout quickly rather
// than blocking handleSend indefinitely. This exercises the timeout
// fall-through path used by handleSend when the runner is unavailable.
func TestWatcherPausePolling_TimeoutFallThrough(t *testing.T) {
	fc := &fakeClient{}
	w := newWatcherForTest("pause-timeout", fc)
	// No goroutine started — pauseCh has no reader.

	start := time.Now()
	rc, err := w.PausePolling(50 * time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrPausePollingTimeout) {
		t.Errorf("got err=%v, want ErrPausePollingTimeout", err)
	}
	if rc != nil {
		t.Errorf("resumeCh must be nil on timeout, got %v", rc)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("PausePolling took %v, want <500ms", elapsed)
	}
}
