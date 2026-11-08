package daemon

import (
	"strings"
	"testing"
)

func TestHandlePruneOnlyRemovesDeadTracking(t *testing.T) {
	d := New()
	w := NewWatcher("dead-one", "pipe", "file", 0, "", nil)
	w.mu.Lock()
	w.status = "dead"
	w.lastError = "NO_TABS"
	w.lastContent = "old one\nold two\nlast line"
	w.mu.Unlock()
	d.mu.Lock()
	d.sessions["dead-one"] = "pipe"
	d.appRuntimes["dead-one"] = "winui3"
	d.watchers["dead-one"] = w
	d.mu.Unlock()

	response := d.handlePrune([]string{"PRUNE", "dead-one"})
	if !strings.HasPrefix(response, "OK|pruned|") {
		t.Fatalf("prune response = %q", response)
	}
	if _, ok := d.resolvePipePath("dead-one"); ok {
		t.Fatal("dead tracking entry remains")
	}
}

func TestListSessionsIncludesDeadDiagnostics(t *testing.T) {
	d := New()
	w := NewWatcher("dead-two", "pipe", "file", 0, "", nil)
	w.mu.Lock()
	w.status = "dead"
	w.lastError = "three failed reads"
	w.lastContent = "one\ntwo\nthree\nfour"
	w.mu.Unlock()
	d.mu.Lock()
	d.sessions["dead-two"] = "pipe"
	d.appRuntimes["dead-two"] = "winui3"
	d.watchers["dead-two"] = w
	d.mu.Unlock()
	entries := d.listSessions()
	if len(entries) != 1 || entries[0].DeadReason != "three failed reads" || entries[0].LastOutput != "two\nthree\nfour" {
		t.Fatalf("dead diagnostics = %+v", entries)
	}
}
