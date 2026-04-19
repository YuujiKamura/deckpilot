package daemon

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestHandleSend_LockTimeoutProtocol verifies the Phase 0.5 (issue #25)
// protocol extension: an optional 5th field SEND|<name>|<base64>|<caller>|<ms>
// switches handleSend from blocking AcquireSendLock to TryAcquireSendLock
// with that timeout.
func TestHandleSend_LockTimeoutProtocol(t *testing.T) {
	d := New()
	session := "lock-timeout-test"

	encoded := base64.StdEncoding.EncodeToString([]byte("msg"))

	// Another caller holds the lock — try-lock path with short timeout
	// must fail fast with ERR|busy|lock_timeout_...
	d.AcquireSendLock(session)
	defer d.ReleaseSendLock(session)

	start := time.Now()
	parts := []string{"SEND", session, encoded, "caller-a", "50"}
	resp := d.handleSend(parts)
	elapsed := time.Since(start)

	if !strings.Contains(resp, "ERR|busy|lock_timeout_50ms") {
		t.Errorf("expected busy error, got %q", resp)
	}
	// Must not block longer than the timeout plus a small scheduling slack.
	if elapsed > 250*time.Millisecond {
		t.Errorf("try-lock took %v, expected <250ms", elapsed)
	}
}

// TestHandleSend_NoTimeoutIsBlocking verifies that when parts[4] is absent
// or zero, handleSend still uses the blocking path. We simulate "blocking"
// by holding the lock, dispatching handleSend on a goroutine, and checking
// it has NOT returned within a short window.
func TestHandleSend_NoTimeoutIsBlocking(t *testing.T) {
	d := New()
	session := "blocking-test"
	encoded := base64.StdEncoding.EncodeToString([]byte("msg"))

	d.AcquireSendLock(session)

	respCh := make(chan string, 1)
	go func() {
		parts := []string{"SEND", session, encoded, "caller-a"}
		respCh <- d.handleSend(parts)
	}()

	select {
	case resp := <-respCh:
		t.Errorf("handleSend returned %q while lock was held — should have blocked", resp)
	case <-time.After(100 * time.Millisecond):
		// Expected: still blocked.
	}

	d.ReleaseSendLock(session)

	// After release it proceeds past the lock. It will fail later because
	// the session is not registered, which is fine for this test — we only
	// care that the lock gate opened.
	select {
	case resp := <-respCh:
		if resp == "" {
			t.Error("expected an error response after unblocking")
		}
		// Expected: ERR|session not found or similar (past the lock gate).
	case <-time.After(1 * time.Second):
		t.Error("handleSend did not unblock after Release")
	}
}

// TestHandleSend_TimeoutParsing verifies that malformed or non-positive
// timeout values fall back to the blocking path (defensive parsing).
func TestHandleSend_TimeoutParsing(t *testing.T) {
	cases := []struct {
		name          string
		timeoutField  string
		expectBlocking bool
	}{
		{"empty string", "", true},
		{"zero", "0", true},
		{"negative", "-100", true},
		{"non-numeric", "abc", true},
		{"positive", "200", false},
	}

	encoded := base64.StdEncoding.EncodeToString([]byte("msg"))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New()
			session := "parse-" + tc.name

			d.AcquireSendLock(session)

			respCh := make(chan string, 1)
			go func() {
				parts := []string{"SEND", session, encoded, "caller-a", tc.timeoutField}
				respCh <- d.handleSend(parts)
			}()

			if tc.expectBlocking {
				// Blocking path → no response within 100ms.
				select {
				case resp := <-respCh:
					t.Errorf("expected blocking, got %q for field=%q", resp, tc.timeoutField)
				case <-time.After(100 * time.Millisecond):
				}
				d.ReleaseSendLock(session)
				<-respCh // drain
			} else {
				// Try-lock path with 200ms timeout → ERR|busy within ~250ms.
				select {
				case resp := <-respCh:
					if !strings.Contains(resp, "ERR|busy|lock_timeout_") {
						t.Errorf("expected busy error for field=%q, got %q", tc.timeoutField, resp)
					}
				case <-time.After(400 * time.Millisecond):
					t.Errorf("try-lock did not fail within budget for field=%q", tc.timeoutField)
				}
				d.ReleaseSendLock(session)
			}
		})
	}
}

// TestHandleSend_InsufficientParts verifies the usage error still fires
// when fewer than 3 parts arrive (SEND|name|msg minimum).
func TestHandleSend_InsufficientParts(t *testing.T) {
	d := New()
	cases := [][]string{
		{"SEND"},
		{"SEND", "name"},
	}
	for _, parts := range cases {
		resp := d.handleSend(parts)
		if !strings.HasPrefix(resp, "ERR|usage:") {
			t.Errorf("parts=%v: expected usage error, got %q", parts, resp)
		}
	}
}

// TestDaemonSendTry_ZeroTimeoutDelegates verifies that DaemonSendTry with
// timeoutMs <= 0 hands off to DaemonSend (i.e. does not attempt to extend
// the protocol). We cannot reach the daemon from a unit test, but we can
// verify behavior-equivalent error text: both paths should fail at the
// pipe-dial stage with identical error shape when no daemon is running.
func TestDaemonSendTry_ZeroTimeoutDelegates(t *testing.T) {
	// Point the daemon pipe at a path that cannot exist so both calls
	// bail out at dialDaemon with matching errors.
	t.Setenv("DECKPILOT_PIPE_SUFFIX", fmt.Sprintf("nonexistent-%d", time.Now().UnixNano()))

	_, errBlocking := DaemonSend("any", "msg", "caller")
	_, errTryZero := DaemonSendTry("any", "msg", "caller", 0)
	_, errTryNeg := DaemonSendTry("any", "msg", "caller", -5)

	if errBlocking == nil || errTryZero == nil || errTryNeg == nil {
		t.Fatal("expected all three calls to error (no daemon)")
	}
	// All three should fail at dial — same error class.
	if !strings.Contains(errBlocking.Error(), "connect to daemon") {
		t.Errorf("blocking err shape unexpected: %v", errBlocking)
	}
	if errTryZero.Error() != errBlocking.Error() {
		t.Errorf("zero-timeout should delegate to DaemonSend: got %v vs %v",
			errTryZero, errBlocking)
	}
	if errTryNeg.Error() != errBlocking.Error() {
		t.Errorf("negative-timeout should delegate to DaemonSend: got %v vs %v",
			errTryNeg, errBlocking)
	}
}
