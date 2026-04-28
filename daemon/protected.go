package daemon

import (
	"fmt"
	"log"
	"os"
)

// Protected sessions (issue #35): an advisory flag that tells the
// daemon "do not kill this session via the cascade kill paths" (i.e.
// `deckpilot kill <name>` without --force, and `deckpilot shutdown`).
// The flag is non-authoritative: any client can call PROTECT /
// UNPROTECT, and `kill --force` bypasses it entirely. The point is to
// avoid stomping a long-running pinned session by accident, not to
// implement real access control.

// Protect adds name to the protected set and persists the change.
// Idempotent: re-protecting an already-protected name is a no-op (no
// extra disk write either, so callers can spam PROTECT cheaply).
func (d *Daemon) Protect(name string) error {
	if name == "" {
		return fmt.Errorf("session name required")
	}
	d.mu.Lock()
	if d.protected == nil {
		d.protected = make(map[string]struct{})
	}
	if _, already := d.protected[name]; already {
		d.mu.Unlock()
		return nil
	}
	d.protected[name] = struct{}{}
	// Snapshot the set so we can release the lock before disk I/O
	// (writeProtectedSessions does a serialize + atomic rename, which
	// can stall for tens of ms on a busy disk).
	snap := make(map[string]struct{}, len(d.protected))
	for k := range d.protected {
		snap[k] = struct{}{}
	}
	d.mu.Unlock()

	if err := writeProtectedSessions(snap); err != nil {
		log.Printf("daemon: protect %s persist warning: %v (in-memory state retained)", name, err)
		return fmt.Errorf("persist: %w", err)
	}
	log.Printf("daemon: protected session %q", name)
	return nil
}

// Unprotect removes name from the protected set and persists.
// Idempotent: unprotecting a non-protected name is a no-op success.
func (d *Daemon) Unprotect(name string) error {
	if name == "" {
		return fmt.Errorf("session name required")
	}
	d.mu.Lock()
	if d.protected == nil {
		d.mu.Unlock()
		return nil
	}
	if _, ok := d.protected[name]; !ok {
		d.mu.Unlock()
		return nil
	}
	delete(d.protected, name)
	snap := make(map[string]struct{}, len(d.protected))
	for k := range d.protected {
		snap[k] = struct{}{}
	}
	d.mu.Unlock()

	if err := writeProtectedSessions(snap); err != nil {
		log.Printf("daemon: unprotect %s persist warning: %v", name, err)
		return fmt.Errorf("persist: %w", err)
	}
	log.Printf("daemon: unprotected session %q", name)
	return nil
}

// IsProtected reports whether name is currently in the protected set.
// Pure read, takes the lock briefly.
func (d *Daemon) IsProtected(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.protected == nil {
		return false
	}
	_, ok := d.protected[name]
	return ok
}

// killSession terminates the OS process backing a session. Honors the
// protected flag unless force is true. Returns a structured error for
// the IPC layer to translate into ERR|protected: ... vs other failures.
//
// Concurrency model: collect (PID, protected) under d.mu in a single
// critical section, release the lock, then issue the kill. This avoids
// holding the daemon mutex across a Windows process API call (which
// can block on an unresponsive child).
func (d *Daemon) killSession(name string, force bool) error {
	if name == "" {
		return fmt.Errorf("session name required")
	}

	d.mu.Lock()
	w, ok := d.watchers[name]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("session not found: %s", name)
	}
	_, isProtected := d.protected[name]
	d.mu.Unlock()

	if isProtected && !force {
		return fmt.Errorf("protected: use --force")
	}

	pid := w.Profile().PID
	if pid <= 0 {
		return fmt.Errorf("session %s has no PID", name)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find pid %d: %w", pid, err)
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	log.Printf("daemon: killed session %q (pid=%d, force=%v)", name, pid, force)
	return nil
}

// snapshotKillTargets returns the names of every live session that is
// NOT protected, captured under a single lock pass so a concurrent
// PROTECT / UNPROTECT cannot race the shutdown fan-out (e.g. a session
// that gets protected mid-iteration must either be in the kill list or
// the protected set — never both, never neither).
func (d *Daemon) snapshotKillTargets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.watchers))
	for name := range d.watchers {
		if _, prot := d.protected[name]; prot {
			continue
		}
		out = append(out, name)
	}
	return out
}
