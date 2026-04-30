package daemon

import (
	"testing"
	"time"
)

func TestAgentToAgentCommunication(t *testing.T) {
	d := New()
	
	// Setup two mock agents
	agentA := NewWatcher("agent-a", "pipe-a", "file-a", 101, nil)
	agentB := NewWatcher("agent-b", "pipe-b", "file-b", 102, nil)
	
	// Initially they are idle (default active, so we set them)
	agentA.mu.Lock()
	agentA.status = "idle"
	agentA.lastPollOK = time.Now()
	agentA.mu.Unlock()
	
	agentB.mu.Lock()
	agentB.status = "active" // Agent B is currently busy
	agentB.lastPollOK = time.Now()
	agentB.mu.Unlock()
	
	d.mu.Lock()
	d.watchers["agent-a"] = agentA
	d.watchers["agent-b"] = agentB
	d.mu.Unlock()
	
	t.Logf("AGENT-TO-AGENT: Agent A sends message to Agent B and requests callback")
	
	// Simulate what happens in handleSubmit:
	// 1. setLastUsed
	d.setLastUsed("pid:101", "agent-b")
	
	// 2. Register callback
	if d.shouldRegisterCallback("agent-b", "agent-a") {
		d.registerCallbackHook("agent-b", "agent-a")
	} else {
		t.Fatal("shouldRegisterCallback failed for agent-a -> agent-b")
	}
	
	// Verify hook is registered
	d.mu.Lock()
	if len(d.idleHooks) != 1 {
		d.mu.Unlock()
		t.Fatalf("expected 1 idle hook, got %d", len(d.idleHooks))
	}
	hook := d.idleHooks[0]
	d.mu.Unlock()
	
	if hook.Type != "callback" || hook.CallbackSession != "agent-a" || hook.SessionFilter != "agent-b" {
		t.Fatalf("incorrect hook registration: %+v", hook)
	}
	
	t.Logf("AGENT-TO-AGENT: Agent B becomes idle, triggering notification to Agent A")
	
	// Simulate Agent B becoming idle
	notification := BufferNotification{
		SessionName:   "agent-b",
		Status:        "idle",
		StatusChanged: true,
		PrevStatus:    "active",
	}
	
	// Agent A should receive notification on its reqCh
	done := make(chan bool)
	go func() {
		select {
		case req := <-agentA.reqCh:
			t.Logf("AGENT-TO-AGENT: Agent A received: %s", req.payload)
			expected := "[NOTIFY] agent-b is idle"
			if req.payload != expected {
				t.Errorf("expected %q, got %q", expected, req.payload)
			}
			req.result <- pipeResult{content: "OK", err: nil}
		case <-time.After(3 * time.Second):
			t.Errorf("Agent A timed out waiting for notification")
		}
		done <- true
	}()
	
	// This normally happens in addSession's notify callback
	d.executeIdleHooks(notification)
	
	<-done
	
	// Wait for one-time hook removal (it has a 100ms delay in executeIdleHooks)
	time.Sleep(200 * time.Millisecond)
	
	// Verify hook was removed (OneTime: true)
	d.mu.Lock()
	count := len(d.idleHooks)
	d.mu.Unlock()
	
	if count != 0 {
		t.Errorf("expected 0 idle hooks after execution, got %d", count)
	}
	
	t.Logf("AGENT-TO-AGENT: Test passed successfully")
}
