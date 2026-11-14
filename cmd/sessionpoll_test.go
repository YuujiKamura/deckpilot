package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// pollKind is the error-class axis of the cartesian: what one DaemonShow
// poll returns.
type pollKind string

const (
	kindNotFound  pollKind = "not-found"
	kindTransient pollKind = "transient"
	kindSuccess   pollKind = "success"
)

var allPollKinds = []pollKind{kindNotFound, kindTransient, kindSuccess}

func (k pollKind) err(session string) error {
	switch k {
	case kindNotFound:
		return daemon.SessionNotFound(session)
	case kindTransient:
		return errors.New("connect to daemon: open \\\\.\\pipe\\deckpilot: The system cannot find the file specified.")
	}
	return nil
}

// countAxis is the consecutive-count axis: 1, exactly the limit, one over.
func countAxis(limit int) []struct {
	name string
	n    int
} {
	return []struct {
		name string
		n    int
	}{
		{"once", 1},
		{"at-limit", limit},
		{"over-limit", limit + 1},
	}
}

// expectedStop returns the 1-based poll index at which the guard must stop
// when fed n polls of kind k, or 0 if it must not stop within n polls.
func expectedStop(k pollKind, n, limit int) int {
	switch k {
	case kindNotFound:
		return 1 // permanent: first poll ends it regardless of n
	case kindTransient:
		if n > limit {
			return limit + 1
		}
	}
	return 0
}

func TestSessionPollGuard_Cartesian(t *testing.T) {
	const limit = 3
	for _, k := range allPollKinds {
		for _, c := range countAxis(limit) {
			k, c := k, c
			t.Run(fmt.Sprintf("%s/%s", k, c.name), func(t *testing.T) {
				g := newSessionPollGuard(limit)
				wantStop := expectedStop(k, c.n, limit)
				nonEmptyRetryLines := 0
				stoppedAt := 0
				for i := 1; i <= c.n; i++ {
					v, line := g.Observe(k.err("ghostty-1"))
					switch {
					case wantStop != 0 && i == wantStop:
						if v != pollStop {
							t.Fatalf("poll %d: verdict=%s, want stop", i, v)
						}
						if line == "" {
							t.Fatalf("poll %d: stop must carry a one-line reason", i)
						}
						if strings.Contains(line, "\n") {
							t.Fatalf("stop line must be one line, got %q", line)
						}
						stoppedAt = i
					case k == kindSuccess:
						if v != pollOK || line != "" {
							t.Fatalf("poll %d: got (%s,%q), want (ok,\"\")", i, v, line)
						}
					default: // transient, within limit
						if v != pollRetry {
							t.Fatalf("poll %d: verdict=%s, want retry", i, v)
						}
						if line != "" {
							nonEmptyRetryLines++
						}
					}
					if stoppedAt != 0 {
						break
					}
				}
				if stoppedAt != wantStop {
					t.Fatalf("stoppedAt=%d, want %d", stoppedAt, wantStop)
				}
				// Identical consecutive transient errors are reported once.
				if k == kindTransient && nonEmptyRetryLines != 1 {
					t.Fatalf("retry lines printed=%d, want 1 (dedup)", nonEmptyRetryLines)
				}
			})
		}
	}
}

func TestSessionPollGuard_RecoveryResetsCountAndDedup(t *testing.T) {
	g := newSessionPollGuard(2)
	transient := kindTransient.err("s")
	g.Observe(transient)
	g.Observe(transient)
	if v, line := g.Observe(nil); v != pollOK || !strings.Contains(line, "recovered after 2") {
		t.Fatalf("recovery: got (%s,%q)", v, line)
	}
	// Counter was reset: two more failures are still within the limit, and
	// the first one is printed again (dedup state was cleared).
	if v, line := g.Observe(transient); v != pollRetry || line == "" {
		t.Fatalf("after recovery first failure: got (%s,%q)", v, line)
	}
	if v, _ := g.Observe(transient); v != pollRetry {
		t.Fatalf("after recovery second failure: got %s, want retry", v)
	}
	if v, _ := g.Observe(transient); v != pollStop {
		t.Fatalf("after recovery third failure: got %s, want stop", v)
	}
}

func TestSessionPollGuard_DistinctTransientErrorsEachPrinted(t *testing.T) {
	g := newSessionPollGuard(5)
	_, l1 := g.Observe(errors.New("no response from daemon"))
	_, l2 := g.Observe(errors.New("no response from daemon"))
	_, l3 := g.Observe(errors.New("write: broken pipe"))
	if l1 == "" || l2 != "" || l3 == "" {
		t.Fatalf("lines = %q / %q / %q; want printed / suppressed / printed", l1, l2, l3)
	}
}

func TestSessionPollGuard_WrappedNotFoundIsPermanent(t *testing.T) {
	g := newSessionPollGuard(10)
	err := fmt.Errorf("list: %w", daemon.SessionNotFound("x"))
	if v, _ := g.Observe(err); v != pollStop {
		t.Fatalf("wrapped not-found: got %s, want stop", v)
	}
	// Plain text that merely looks like not-found is NOT classified by
	// string matching; it counts as transient.
	g2 := newSessionPollGuard(10)
	if v, _ := g2.Observe(errors.New("session not found: x")); v != pollRetry {
		t.Fatalf("untyped text: got %s, want retry", v)
	}
}

func TestTransientLimitFor(t *testing.T) {
	cases := []struct {
		interval, budget time.Duration
		want             int
	}{
		{3 * time.Second, 60 * time.Second, 20},
		{2 * time.Second, 60 * time.Second, 30},
		{7 * time.Second, 60 * time.Second, 9}, // ceil(60/7)
		{time.Minute, 60 * time.Second, minTransientFailures},
		{10 * time.Minute, 60 * time.Second, minTransientFailures},
		{0, 60 * time.Second, minTransientFailures},
	}
	for _, c := range cases {
		if got := transientLimitFor(c.interval, c.budget); got != c.want {
			t.Errorf("transientLimitFor(%s,%s)=%d, want %d", c.interval, c.budget, got, c.want)
		}
	}
}

// runAutoApprovalsWith drives runAutoApprovals with a scripted DaemonShow.
// After the script is exhausted every poll returns not-found, so a loop that
// fails to stop on its own still terminates (and the call count shows where
// it stopped).
func runAutoApprovalsWith(t *testing.T, script []error, limit int) (code, calls int, out string) {
	t.Helper()
	const session = "ghostty-36764"
	origShow, origSend := autoApprovalsShow, autoApprovalsSend
	t.Cleanup(func() { autoApprovalsShow, autoApprovalsSend = origShow, origSend })

	autoApprovalsShow = func(name, mode, caller string) (string, string, error) {
		calls++
		if calls <= len(script) {
			if err := script[calls-1]; err != nil {
				return "", "", err
			}
			return "idle prompt, nothing to approve\n", "idle", nil
		}
		return "", "", daemon.SessionNotFound(name)
	}
	autoApprovalsSend = func(name, msg, caller string) (string, error) {
		t.Fatalf("unexpected send to %s", name)
		return "", nil
	}

	tick := make(chan time.Time, len(script)+5)
	for i := 0; i < cap(tick); i++ {
		tick <- time.Time{}
	}
	stop := make(chan os.Signal)
	var buf bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runAutoApprovals(autoApprovalsConfig{session: session, maxTransient: limit}, tick, stop, &buf)
	}()
	select {
	case code = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runAutoApprovals did not return (still looping)")
	}
	return code, calls, buf.String()
}

func TestRunAutoApprovals_Cartesian(t *testing.T) {
	const limit = 3
	for _, k := range allPollKinds {
		for _, c := range countAxis(limit) {
			k, c := k, c
			t.Run(fmt.Sprintf("%s/%s", k, c.name), func(t *testing.T) {
				script := make([]error, c.n)
				for i := range script {
					script[i] = k.err("ghostty-36764")
				}
				code, calls, out := runAutoApprovalsWith(t, script, limit)

				if code != 1 {
					t.Fatalf("exit code=%d, want 1\n%s", code, out)
				}
				// Where it must stop: inside the script for permanent /
				// over-limit, else on the trailing not-found poll.
				wantCalls := c.n + 1
				if s := expectedStop(k, c.n, limit); s != 0 {
					wantCalls = s
				}
				if calls != wantCalls {
					t.Fatalf("DaemonShow calls=%d, want %d\n%s", calls, wantCalls, out)
				}
				lines := strings.Split(strings.TrimSpace(out), "\n")
				last := lines[len(lines)-1]
				if k == kindTransient && c.n > limit {
					if !strings.Contains(last, "giving up after 4 consecutive failures") {
						t.Fatalf("last line=%q, want give-up", last)
					}
				} else if !strings.Contains(last, "session not found: ghostty-36764 (session is gone, stopping)") {
					t.Fatalf("last line=%q, want session-gone", last)
				}
				if got := strings.Count(out, "(retrying"); k == kindTransient && got != 1 {
					t.Fatalf("retrying lines=%d, want 1 (dedup)\n%s", got, out)
				} else if k != kindTransient && got != 0 {
					t.Fatalf("retrying lines=%d, want 0\n%s", got, out)
				}
			})
		}
	}
}

func TestRunAutoApprovals_InterruptExitsZero(t *testing.T) {
	origShow := autoApprovalsShow
	t.Cleanup(func() { autoApprovalsShow = origShow })
	autoApprovalsShow = func(string, string, string) (string, string, error) {
		t.Fatal("no poll expected")
		return "", "", nil
	}
	stop := make(chan os.Signal, 1)
	stop <- os.Interrupt
	var buf bytes.Buffer
	if code := runAutoApprovals(autoApprovalsConfig{session: "s", maxTransient: 3}, make(chan time.Time), stop, &buf); code != 0 {
		t.Fatalf("interrupt exit code=%d, want 0", code)
	}
}

func TestSuperviseSessionPresence(t *testing.T) {
	t.Run("gone stops with true", func(t *testing.T) {
		var buf bytes.Buffer
		stopped := superviseSessionPresence(context.Background(), func() error {
			return daemon.SessionNotFound("g")
		}, time.Millisecond, newSessionPollGuard(3), &buf, "hang-detect")
		if !stopped || !strings.HasPrefix(buf.String(), "hang-detect: session not found: g") {
			t.Fatalf("stopped=%v out=%q", stopped, buf.String())
		}
	})
	t.Run("transient over limit stops with true", func(t *testing.T) {
		var buf bytes.Buffer
		calls := 0
		stopped := superviseSessionPresence(context.Background(), func() error {
			calls++
			return errors.New("no response from daemon")
		}, time.Millisecond, newSessionPollGuard(2), &buf, "hang-detect")
		if !stopped || calls != 3 {
			t.Fatalf("stopped=%v calls=%d", stopped, calls)
		}
	})
	t.Run("cancel returns false", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		res := make(chan bool, 1)
		go func() {
			res <- superviseSessionPresence(ctx, func() error {
				calls++
				if calls == 3 {
					cancel()
				}
				return nil
			}, time.Millisecond, newSessionPollGuard(2), &bytes.Buffer{}, "x")
		}()
		select {
		case got := <-res:
			if got {
				t.Fatal("cancelled supervisor must return false")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("supervisor did not return after cancel")
		}
	})
}
