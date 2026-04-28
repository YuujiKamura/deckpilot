package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// Protected sessions are persisted to disk so they survive daemon restarts
// (issue #35). Storage layout: a single JSON object at
// ~/.deckpilot/protected-sessions.json with the schema {"protected": [...]}.
//
// Single file (instead of a directory of one-file-per-session, like idle
// hooks) because the data is just a flat name set: no per-entry metadata
// to grow into a sidecar JSON, and the operator should be able to grep /
// hand-edit the whole list in one place. The trade-off is that mutations
// rewrite the whole file, which is fine at the expected cardinality
// (operators protect a handful of sessions, not thousands).
//
// Concurrency: in-memory mutation under Daemon.mu, file I/O outside the
// lock — same pattern as idle_hooks_persist.go. A torn write is avoided
// by writing to <path>.tmp and renaming over the target.

// protectedSessionsFile returns the on-disk path for the protected list.
// Reuses the daemonUserHome seam from idle_hooks_persist.go so tests can
// redirect both feature areas with a single override.
func protectedSessionsFile() string {
	home, err := daemonUserHome()
	if err == nil {
		return filepath.Join(home, ".deckpilot", "protected-sessions.json")
	}
	return filepath.Join(os.TempDir(), "deckpilot", "protected-sessions.json")
}

// protectedSessionsDoc is the on-disk JSON shape. Top-level object so we
// can grow extra fields (timestamp, reason, etc.) without breaking
// readers — clients only key off "protected".
type protectedSessionsDoc struct {
	Protected []string `json:"protected"`
}

// loadProtectedSessions reads the on-disk list and returns it as a set.
// Best-effort: a missing file returns an empty set with no error, and
// malformed JSON is logged + treated as empty so the daemon can always
// start. Never fail-fast.
func loadProtectedSessions() map[string]struct{} {
	out := make(map[string]struct{})
	path := protectedSessionsFile()
	body, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("daemon: protected-sessions read %s: %v (treating as empty)", path, err)
		}
		return out
	}
	var doc protectedSessionsDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		log.Printf("daemon: protected-sessions parse %s: %v (treating as empty)", path, err)
		return out
	}
	for _, name := range doc.Protected {
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// writeProtectedSessions persists the given set atomically. The write
// goes to <path>.tmp first; on success we remove the existing target
// (ignoring ENOENT) and rename over it. This mirrors writeIdleHookFile
// but avoids the torn-write window because the protected list is a
// single document, not a per-entry sidecar.
func writeProtectedSessions(set map[string]struct{}) error {
	path := protectedSessionsFile()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("protected-sessions mkdir: %w", err)
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	doc := protectedSessionsDoc{Protected: names}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("protected-sessions marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("protected-sessions write tmp %s: %w", tmp, err)
	}
	// Windows rename is non-atomic when the target exists, so remove
	// first; ignore ENOENT because a fresh install has no target.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// Don't abort: rename below will surface a real failure.
		log.Printf("daemon: protected-sessions remove old %s: %v (continuing)", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("protected-sessions rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
