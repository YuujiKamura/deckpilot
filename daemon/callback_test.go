package daemon

import (
	"testing"
	"time"
)

func TestExecuteCallbackHook(t *testing.T) {
	d := New()
	
	// Create a mock caller watcher
	// Note: pipePath and sessionFile don't matter as we won't run the watcher's Run loop
	callerWatcher := NewWatcher("caller_session", "fake_pipe", "fake_file", 123, "", nil)
	d.mu.Lock()
	d.watchers["caller_session"] = callerWatcher
	d.mu.Unlock()
	
	hook := IdleHook{
		Type:            "callback",
		CallbackSession: "caller_session",
	}
	
	notification := BufferNotification{
		SessionName: "target_session",
		Status:      "idle",
	}
	
	// Start a goroutine to handle the Send request from executeCallbackHook
	done := make(chan bool)
	go func() {
		t.Logf("DEBUG: Waiting for request on reqCh for caller_session...")
		select {
		case req := <-callerWatcher.reqCh:
			t.Logf("DEBUG: Received request: kind=%s, payload=%q", req.kind, req.payload)
			if req.kind != "sendkeys" {
				t.Errorf("expected kind 'sendkeys', got %q", req.kind)
			}
			expectedMsg := "[NOTIFY] target_session is idle\n"
			if req.payload != expectedMsg {
				t.Errorf("expected payload %q, got %q", expectedMsg, req.payload)
			}
			// Send back result to unblock Send()
			t.Logf("DEBUG: Sending back success result to unblock Send()")
			req.result <- pipeResult{content: "OK", err: nil}
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for request on reqCh")
		}
		done <- true
	}()
	
	t.Logf("DEBUG: Calling executeCallbackHook for target_session...")
	err := d.executeCallbackHook(hook, notification)
	if err != nil {
		t.Fatalf("executeCallbackHook failed: %v", err)
	}
	t.Logf("DEBUG: executeCallbackHook returned successfully")
	
	<-done
	t.Logf("DEBUG: Test handles asynchronous callback correctly")
}

func TestResolveCallerSession(t *testing.T) {
	d := New()
	
	// Create two sessions
	s1 := NewWatcher("session1", "p1", "f1", 1, "", nil)
	s2 := NewWatcher("session2", "p2", "f2", 2, "", nil)

	// Default status is "active", set them to "idle" initially
	s1.mu.Lock()
	s1.status = "idle"
	s1.mu.Unlock()
	s2.mu.Lock()
	s2.status = "idle"
	s2.mu.Unlock()
	
	d.mu.Lock()
	d.watchers["session1"] = s1
	d.watchers["session2"] = s2
	d.mu.Unlock()
	
	// 1. Exact match
	if res := d.resolveCallerSession("session1"); res != "session1" {
		t.Errorf("expected session1, got %q", res)
	}
	
	// 2. Heuristic: active session
	s2.mu.Lock()
	s2.status = "active"
	s2.mu.Unlock()
	
	if res := d.resolveCallerSession("pid:1234"); res != "session2" {
		t.Errorf("expected session2 (active), got %q", res)
	}
	
	// 3. Heuristic: recent activity (no active sessions)
	s2.mu.Lock()
	s2.status = "idle"
	s2.mu.Unlock()
	
	now := time.Now()
	s1.mu.Lock()
	s1.lastPollOK = now.Add(-10 * time.Second)
	s1.mu.Unlock()
	
	s2.mu.Lock()
	s2.lastPollOK = now.Add(-5 * time.Second)
	s2.mu.Unlock()
	
	if res := d.resolveCallerSession("pid:1234"); res != "session2" {
		t.Errorf("expected session2 (recent activity), got %q", res)
	}
}

func TestShouldRegisterCallback(t *testing.T) {
	d := New()
	
	// Create sessions
	s1 := NewWatcher("session1", "p1", "f1", 1, "", nil)
	s2 := NewWatcher("session2", "p2", "f2", 2, "", nil)

	d.mu.Lock()
	d.watchers["session1"] = s1
	d.watchers["session2"] = s2
	d.mu.Unlock()

	// 1. Same session - should NOT register
	if d.shouldRegisterCallback("session1", "session1") {
		t.Error("should NOT register callback for same session")
	}
	
	// 2. Target doesn't exist - should NOT register
	if d.shouldRegisterCallback("non_existent", "session1") {
		t.Error("should NOT register callback for non-existent target")
	}
	
	// 3. Different sessions - SHOULD register
	if !d.shouldRegisterCallback("session2", "session1") {
		t.Error("SHOULD register callback for different sessions")
	}
}

// TestShouldRegisterCallback_RejectsHeuristicSelfLoop reproduces issue #19.
//
// When the raw caller ID (e.g. wt:<GUID> from an external shell that is NOT a
// registered watcher) cannot be matched to a watcher, resolveCallerSession
// falls back to "most recently active watcher". Immediately after send, that
// watcher IS the target itself. shouldRegisterCallback must refuse this
// self-loop; otherwise executeCallbackHook writes [NOTIFY] target is idle
// into the target's own input pipe, and the real external caller receives
// nothing.
func TestShouldRegisterCallback_RejectsHeuristicSelfLoop(t *testing.T) {
	d := New()

	target := NewWatcher("ghostty-71964", "pt", "ft", 1, "", nil)
	// Simulate "just got a send" — target is the most recently active watcher.
	target.mu.Lock()
	target.status = "active"
	target.lastPollOK = time.Now()
	target.mu.Unlock()

	d.mu.Lock()
	d.watchers["ghostty-71964"] = target
	d.mu.Unlock()

	// Raw caller is an external WT GUID with no matching watcher.
	// Today resolveCallerSession returns "ghostty-71964" (heuristic) and
	// shouldRegisterCallback accepts it -> self-loop hook gets registered.
	if d.shouldRegisterCallback("ghostty-71964", "wt:external-claude-code-guid") {
		t.Fatalf("self-loop: shouldRegisterCallback accepted a caller that heuristically resolves to the target; " +
			"this registers callback_session == target and notifies the target's own pipe (issue #19)")
	}
}
