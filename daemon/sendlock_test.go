package daemon

import (
	"sync"
	"testing"
	"time"
)

// T1 newSendLockRegistry
func TestNewSendLockRegistry(t *testing.T) {
	r := newSendLockRegistry()
	if r == nil {
		t.Fatal("expected non-nil registry")
	}
	if r.locks == nil {
		t.Fatal("expected non-nil locks map")
	}
	if len(r.locks) != 0 {
		t.Errorf("expected empty locks map, got %d", len(r.locks))
	}
}

// T2 getOrCreate
func TestGetOrCreate(t *testing.T) {
	r := newSendLockRegistry()
	
	m1 := r.getOrCreate("session1")
	m2 := r.getOrCreate("session1")
	if m1 != m2 {
		t.Error("expected same mutex for same name")
	}
	
	m3 := r.getOrCreate("session2")
	if m1 == m3 {
		t.Error("expected different mutex for different name")
	}
	
	mEmpty := r.getOrCreate("")
	if mEmpty == nil {
		t.Error("expected non-nil mutex for empty name")
	}
	
	// concurrent
	const n = 100
	var wg sync.WaitGroup
	results := make([]*sync.Mutex, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = r.getOrCreate("concurrent")
		}(i)
	}
	wg.Wait()
	
	first := results[0]
	for i, res := range results {
		if res != first {
			t.Errorf("index %d got different mutex", i)
		}
	}
}

// T3 tryLockOnce
func TestTryLockOnce(t *testing.T) {
	m := &sync.Mutex{}
	
	if !tryLockOnce(m) {
		t.Error("expected to acquire unlocked mutex")
	}
	// m is now locked
	
	if tryLockOnce(m) {
		t.Error("expected to fail acquiring locked mutex")
	}
	
	m.Unlock()
	if !tryLockOnce(m) {
		t.Error("expected to acquire after unlock")
	}
	m.Unlock()
}

// T4 Daemon.AcquireSendLock / ReleaseSendLock
func TestAcquireRelease(t *testing.T) {
	d := New()
	
	// Normal flow
	d.AcquireSendLock("s1")
	d.ReleaseSendLock("s1")
	
	// Late init test (zero value Daemon)
	d2 := &Daemon{}
	d2.AcquireSendLock("s1")
	d2.ReleaseSendLock("s1")
	
	// Blocking test
	d3 := New()
	d3.AcquireSendLock("block")
	
	done := make(chan bool)
	go func() {
		d3.AcquireSendLock("block")
		d3.ReleaseSendLock("block")
		done <- true
	}()
	
	select {
	case <-done:
		t.Error("second Acquire should have blocked")
	case <-time.After(100 * time.Millisecond):
		// blocked as expected
	}
	
	d3.ReleaseSendLock("block")
	
	select {
	case <-done:
		// unblocked
	case <-time.After(500 * time.Millisecond):
		t.Error("second Acquire did not unblock after Release")
	}
	
	// Different names concurrent
	d4 := New()
	d4.AcquireSendLock("n1")
	
	start := time.Now()
	d4.AcquireSendLock("n2") // should not block
	d4.ReleaseSendLock("n2")
	if time.Since(start) > 100*time.Millisecond {
		t.Errorf("Acquire for different name should not have blocked, took %v", time.Since(start))
	}
	d4.ReleaseSendLock("n1")
}

// T5 TryAcquireSendLock
func TestTryAcquireSendLock(t *testing.T) {
	d := New()
	name := "try"
	
	// Unlocked
	if !d.TryAcquireSendLock(name, 0) {
		t.Error("expected success with timeout 0 on unlocked")
	}
	d.ReleaseSendLock(name)
	
	if !d.TryAcquireSendLock(name, 100*time.Millisecond) {
		t.Error("expected success with timeout on unlocked")
	}
	d.ReleaseSendLock(name)
	
	// Locked
	d.AcquireSendLock(name)
	
	if d.TryAcquireSendLock(name, 0) {
		t.Error("expected failure with timeout 0 on locked")
	}
	
	start := time.Now()
	if d.TryAcquireSendLock(name, 50*time.Millisecond) {
		t.Error("expected failure with short timeout on locked")
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Errorf("TryAcquire returned too early: %v", time.Since(start))
	}
	
	// Successful retry
	go func() {
		time.Sleep(30 * time.Millisecond)
		d.ReleaseSendLock(name)
	}()
	
	if !d.TryAcquireSendLock(name, 100*time.Millisecond) {
		t.Error("expected success after concurrent release")
	}
	d.ReleaseSendLock(name)
	
	// Negative timeout
	d.AcquireSendLock(name)
	if d.TryAcquireSendLock(name, -1*time.Second) {
		t.Error("expected failure with negative timeout on locked")
	}
	d.ReleaseSendLock(name)
}

// T6 sendLocksInit
func TestSendLocksInit(t *testing.T) {
	// New() case
	d := New()
	original := d.sendLocks
	d.sendLocksInit()
	if d.sendLocks != original {
		t.Error("sendLocksInit should not replace existing registry")
	}
	
	// Zero value case
	d2 := &Daemon{}
	d2.sendLocksInit()
	if d2.sendLocks == nil {
		t.Fatal("sendLocksInit failed to initialize")
	}
	
	// Concurrency
	d3 := &Daemon{}
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			d3.sendLocksInit()
		}()
	}
	wg.Wait()
	if d3.sendLocks == nil {
		t.Error("sendLocks is nil after concurrent init")
	}
}

// T7 Integration: handleSend の直列化
func TestSendLockIntegration(t *testing.T) {
	d := New()
	
	// Same session
	var wg sync.WaitGroup
	wg.Add(2)
	
	start := time.Now()
	go func() {
		defer wg.Done()
		d.AcquireSendLock("session-x")
		time.Sleep(100 * time.Millisecond)
		d.ReleaseSendLock("session-x")
	}()
	
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond) // Ensure the first goroutine wins
		d.AcquireSendLock("session-x")
		d.ReleaseSendLock("session-x")
	}()
	
	wg.Wait()
	duration := time.Since(start)
	if duration < 100*time.Millisecond {
		t.Errorf("expected serialization for same session, took only %v", duration)
	}

	// Different sessions
	start = time.Now()
	wg.Add(2)
	go func() {
		defer wg.Done()
		d.AcquireSendLock("session-a")
		time.Sleep(100 * time.Millisecond)
		d.ReleaseSendLock("session-a")
	}()
	
	go func() {
		defer wg.Done()
		d.AcquireSendLock("session-b")
		time.Sleep(100 * time.Millisecond)
		d.ReleaseSendLock("session-b")
	}()
	
	wg.Wait()
	duration = time.Since(start)
	if duration > 150*time.Millisecond {
		t.Errorf("expected parallel execution for different sessions, took %v", duration)
	}
}
