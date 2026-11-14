package daemon

import (
	"testing"
	"time"
)

// The notify callback takes the daemon lock, and the daemon reads watcher
// state (Status / Profile) while holding that lock. If updateContent called
// the callback with the watcher lock held, the two would deadlock and every
// deckpilot command would get "no response from daemon" until the
// self-watchdog killed the process (seen twice on 2026-11-15).
func TestUpdateContentNotifiesOutsideTheWatcherLock(t *testing.T) {
	var w *Watcher
	got := make(chan string, 4)
	w = NewWatcher("s", "", "", 0, "", func(n BufferNotification) {
		// Both take w.mu; they block forever if the caller still holds it.
		_ = w.Profile()
		got <- w.Status()
	})
	go w.updateContent("first screen")
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("updateContent held the watcher lock while calling the notify callback")
	}
}
