// Package daemon — send serialization primitive (Issue #25 Layer 0).
//
// handleSend executes a 4-phase sequence on a shared PTY byte stream
// (baseline snapshot → INPUT typing → Enter → ConfirmSubmit polling).
// Auto-approvals can issue its own SEND (an empty-message Enter) to the
// same session at any moment, which races the user send's typing phase
// and corrupts the TUI input buffer. See
// https://github.com/YuujiKamura/deckpilot/issues/25 for the failure modes
// observed on 2026-04-19 (Codex 35572 hang, Gemini 35984 introspection
// loop).
//
// This file provides per-session mutexes so that the full handleSend
// logical command (a command string + LF, verified) is atomic against
// any other SEND targeting the same session.
package daemon

import (
	"sync"
	"time"
)

// sendLockRegistry holds one mutex per session name. The registry's own
// mutex (mu) protects the map itself; the per-session mutex protects the
// handleSend critical section.
type sendLockRegistry struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// newSendLockRegistry returns an initialized registry.
func newSendLockRegistry() *sendLockRegistry {
	return &sendLockRegistry{locks: make(map[string]*sync.Mutex)}
}

// getOrCreate returns the mutex for the given session name, allocating
// on first use. Safe for concurrent callers.
func (r *sendLockRegistry) getOrCreate(name string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.locks[name]
	if !ok {
		m = &sync.Mutex{}
		r.locks[name] = m
	}
	return m
}

// AcquireSendLock blocks until the send lock for the given session is
// held by the caller. Use defer ReleaseSendLock(name) for symmetry.
func (d *Daemon) AcquireSendLock(name string) {
	d.sendLocksInit()
	d.sendLocks.getOrCreate(name).Lock()
}

// TryAcquireSendLock attempts to acquire the send lock within the given
// timeout. Returns true when the lock is held, false when the timeout
// expired without acquisition. A zero or negative timeout performs a
// single non-blocking attempt.
//
// This is the approve-side entry point: the auto-approvals loop should
// bail out (and retry on the next tick) rather than queue behind a long
// user send.
func (d *Daemon) TryAcquireSendLock(name string, timeout time.Duration) bool {
	d.sendLocksInit()
	m := d.sendLocks.getOrCreate(name)

	if timeout <= 0 {
		return tryLockOnce(m)
	}

	// Poll in short slices. sync.Mutex has no native TryLock with timeout
	// in Go 1.18+; TryLock exists but is non-blocking. We poll to keep the
	// implementation compatible with the toolchain declared in go.mod.
	deadline := time.Now().Add(timeout)
	for {
		if tryLockOnce(m) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ReleaseSendLock releases the send lock for the given session. Must be
// called exactly once per successful Acquire/TryAcquire returning true.
func (d *Daemon) ReleaseSendLock(name string) {
	d.sendLocksInit()
	d.sendLocks.getOrCreate(name).Unlock()
}

// tryLockOnce is a tiny wrapper around sync.Mutex.TryLock so tests can
// substitute behavior if needed. Returns true iff the lock was acquired.
func tryLockOnce(m *sync.Mutex) bool {
	return m.TryLock()
}

// sendLocksInit lazy-initializes d.sendLocks. Daemons created via New()
// will already have it initialized; this guards the zero-value path used
// by some tests that construct a Daemon directly.
func (d *Daemon) sendLocksInit() {
	d.sendLocksInitOnce.Do(func() {
		if d.sendLocks == nil {
			d.sendLocks = newSendLockRegistry()
		}
	})
}
