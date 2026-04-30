package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Idle hooks are persisted to disk so they survive daemon restarts.
// Storage layout mirrors launch-meta (cmd/launchmeta.go): one JSON file
// per hook under ~/.deckpilot/idle-hooks/<id>.json.
//
// Why a sidecar JSON tree instead of widening the IPC surface:
//   - The daemon's idleHooks slice is in-memory; restarts wipe it,
//     forcing the operator to re-run `deckpilot notify add ...` each
//     time the daemon comes back up. That defeats the whole point of
//     hooks as a long-lived configuration.
//   - The launch-meta refactor (commit 2f7b79a) already proved the
//     pattern works without changing IPC: callers keep the existing
//     HOOK / HOOK_LIST envelope, and the daemon just learned to load
//     and write a directory at boot / mutation time.
//
// Package boundary note: cmd/paths.go has its own deckpilotHomeDir()
// for CLI-side path resolution, but having daemon import cmd would
// create a cycle (cmd already imports daemon for IPC helpers). The
// daemon therefore keeps its own UserHomeDir seam below; the small
// duplication is acceptable to preserve the package boundary.

// daemonUserHome is the test seam for the daemon's home-dir lookup.
// Independent from cmd/paths.go's deckpilotUserHome on purpose: the
// two packages must not import each other.
var daemonUserHome = os.UserHomeDir

// idleHooksDir returns ~/.deckpilot/idle-hooks (or the %TEMP% fallback
// if HOME / USERPROFILE are unresolvable, matching cmd/paths.go's
// deckpilotHomeDir() behavior).
func idleHooksDir() string {
	home, err := daemonUserHome()
	if err == nil {
		return filepath.Join(home, ".deckpilot", "idle-hooks")
	}
	return filepath.Join(os.TempDir(), "deckpilot", "idle-hooks")
}

// generateHookID returns a unique hook ID. Uses timestamp (ns) plus
// 8 random bytes so concurrent AddIdleHook calls cannot collide even
// if the wall-clock resolution is coarse on Windows.
func generateHookID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to ns timestamp alone — never block AddIdleHook on
		// PRNG failure (which on Windows is essentially impossible but
		// must not panic the daemon either).
		return fmt.Sprintf("hook-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("hook-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

// idleHookPath returns the on-disk path for the given hook id.
func idleHookPath(id string) string {
	return filepath.Join(idleHooksDir(), id+".json")
}

// writeIdleHookFile persists a single hook. Best-effort: returns the
// error so callers can log a warning, but AddIdleHook must not fail
// just because the disk is read-only.
func writeIdleHookFile(hook IdleHook) error {
	if hook.ID == "" {
		return fmt.Errorf("writeIdleHookFile: hook missing ID")
	}
	dir := idleHooksDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("idle-hook mkdir: %w", err)
	}
	body, err := json.MarshalIndent(hook, "", "  ")
	if err != nil {
		return fmt.Errorf("idle-hook marshal: %w", err)
	}
	path := idleHookPath(hook.ID)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("idle-hook write %s: %w", path, err)
	}
	return nil
}

// removeIdleHookFile deletes the on-disk record for hook id. Missing
// files are not an error — the in-memory state is the source of truth
// at fire time, the file is just persistence.
func removeIdleHookFile(id string) error {
	if id == "" {
		return nil
	}
	path := idleHookPath(id)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("idle-hook remove %s: %w", path, err)
	}
	return nil
}

// removeAllIdleHookFiles wipes every persisted hook in idleHooksDir.
// Used by RemoveIdleHooks. A missing directory is treated as a no-op
// (matches the "first-call-on-fresh-install" pattern from cleanup.go).
// Per-file errors are logged and skipped — one corrupt file must not
// prevent the rest of the wipe.
func removeAllIdleHookFiles() {
	dir := idleHooksDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("daemon: idle-hook list dir %s: %v", dir, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err != nil {
			log.Printf("daemon: idle-hook remove %s: %v", path, err)
		}
	}
}

// loadIdleHooks reads every *.json file under idleHooksDir and returns
// the hooks. Best-effort: a missing directory yields an empty slice
// with no error, and individual parse failures are skipped with a
// warning so the daemon can always start.
func loadIdleHooks() []IdleHook {
	dir := idleHooksDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("daemon: idle-hook load dir %s: %v", dir, err)
		}
		return nil
	}
	out := make([]IdleHook, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		path := filepath.Join(dir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			log.Printf("daemon: idle-hook read %s: %v", path, err)
			continue
		}
		var hook IdleHook
		if err := json.Unmarshal(body, &hook); err != nil {
			log.Printf("daemon: idle-hook parse %s: %v (skipping)", path, err)
			continue
		}
		// Backfill ID from filename if the file was hand-edited and the
		// embedded ID got lost.
		if hook.ID == "" {
			hook.ID = name[:len(name)-len(".json")]
		}
		out = append(out, hook)
	}
	return out
}
