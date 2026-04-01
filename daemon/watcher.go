package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

// Watcher monitors a single Ghostty session by polling its terminal output.
type Watcher struct {
	mu          sync.Mutex
	name        string
	pipePath    string
	lastHash    string
	stableCount int
	status      string // "idle", "active", "dead"
	lastContent string
}

// NewWatcher creates a Watcher for the given session.
func NewWatcher(name, pipePath string) *Watcher {
	return &Watcher{
		name:     name,
		pipePath: pipePath,
		status:   "active",
	}
}

// Run polls the session output every 500ms. If ctx is nil, it runs until
// the session is detected as dead.
func (w *Watcher) Run(ctx context.Context) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll()
			if w.Status() == "dead" {
				log.Printf("watcher: session %q is dead, stopping", w.name)
				return
			}
		}
	}
}

func (w *Watcher) poll() {
	content, err := pipe.Tail(w.pipePath, 50)
	if err != nil {
		w.mu.Lock()
		w.status = "dead"
		w.mu.Unlock()
		return
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	w.mu.Lock()
	defer w.mu.Unlock()

	w.lastContent = content

	if hash == w.lastHash {
		w.stableCount++
		if w.stableCount >= 3 {
			w.status = "idle"
		}
	} else {
		w.lastHash = hash
		w.stableCount = 0
		w.status = "active"
	}
}

// Status returns the current status: "idle", "active", or "dead".
func (w *Watcher) Status() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// LastContent returns the last polled terminal content.
func (w *Watcher) LastContent() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastContent
}
