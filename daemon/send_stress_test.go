package daemon

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestSendStress_TimeoutBypass verifies Phase 0.5 (Issue #25) fix:
// Auto-approvals should skip if the lock is held by a user send.
func TestSendStress_TimeoutBypass(t *testing.T) {
	d := New()
	session := "timeout-session"
	
	// 1. Hold the lock manually to simulate a long user send (e.g. ConfirmSubmit polling)
	d.AcquireSendLock(session)
	
	// 2. Attempt a SEND with a short timeout (simulating Phase 0.5 auto-approvals)
	start := time.Now()
	resp := d.handleSend([]string{"SEND", session, base64.StdEncoding.EncodeToString([]byte("")), "auto-approvals", "50"})
	elapsed := time.Since(start)
	
	if !strings.Contains(resp, "ERR|busy|lock_timeout_50ms") {
		t.Errorf("Expected timeout error, got %q", resp)
	}
	
	if elapsed < 50*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Errorf("Timeout duration out of bounds: %v", elapsed)
	}
	
	d.ReleaseSendLock(session)
	
	// 3. Now it should succeed
	resp2 := d.handleSend([]string{"SEND", session, base64.StdEncoding.EncodeToString([]byte("")), "auto-approvals", "50"})
	if strings.Contains(resp2, "ERR|busy") {
		t.Errorf("Expected success after release, got %q", resp2)
	}
}

// TestMultiAgent_Isolation verifies that locking one session doesn't block another.
func TestMultiAgent_Isolation(t *testing.T) {
	d := New()
	
	d.AcquireSendLock("agent-claude")
	
	start := time.Now()
	// Sending to gemini should be instantaneous (it fails fast because session not found,
	// but the point is it doesn't wait for the 'agent-claude' lock).
	d.handleSend([]string{"SEND", "agent-gemini", base64.StdEncoding.EncodeToString([]byte("ping")), "tester", "100"})
	
	if time.Since(start) > 50*time.Millisecond {
		t.Errorf("Gemini session blocked by Claude lock! Took %v", time.Since(start))
	}
	
	d.ReleaseSendLock("agent-claude")
}
