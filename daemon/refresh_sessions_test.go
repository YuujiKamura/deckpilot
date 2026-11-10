package daemon

import "testing"

func TestRefreshSessionsPrunesActiveSessionWithGonePID(t *testing.T) {
	d := New()
	w := NewWatcher("ghostty-2147483647", "pipe", "file", 2147483647, "", nil)
	d.sessions[w.name] = "pipe"
	d.watchers[w.name] = w
	d.appRuntimes[w.name] = "winui3"

	d.refreshSessions()

	if _, ok := d.sessions[w.name]; ok {
		t.Fatal("session with a confirmed-gone PID remains tracked")
	}
	if _, ok := d.watchers[w.name]; ok {
		t.Fatal("watcher with a confirmed-gone PID remains tracked")
	}
}
