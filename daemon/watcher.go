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

// BufferNotification is emitted when the watcher detects a change or stability.
type BufferNotification struct {
	SessionName string
	Content     string
	Hash        string
	Changed     bool   // hash changed since last poll
	StableFor   int    // consecutive identical reads
	Status      string // "idle", "active", "dead"
}

// Watcher monitors a single Ghostty session by polling its terminal output.
type Watcher struct {
	mu          sync.Mutex
	name        string
	pipePath    string
	lastHash    string
	stableCount int
	status      string // "idle", "active", "dead"
	lastContent string
	onNotify    func(BufferNotification) // optional callback
}

// NewWatcher creates a Watcher for the given session.
// onNotify is called on every poll cycle (may be nil).
func NewWatcher(name, pipePath string, onNotify func(BufferNotification)) *Watcher {
	return &Watcher{
		name:     name,
		pipePath: pipePath,
		status:   "active",
		onNotify: onNotify,
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

	changed := hash != w.lastHash
	if changed {
		w.lastHash = hash
		w.stableCount = 0
		w.status = "active"
	} else {
		w.stableCount++
		if w.stableCount >= 3 {
			w.status = "idle"
		}
	}

	if w.onNotify != nil {
		w.onNotify(BufferNotification{
			SessionName: w.name,
			Content:     content,
			Hash:        hash,
			Changed:     changed,
			StableFor:   w.stableCount,
			Status:      w.status,
		})
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
