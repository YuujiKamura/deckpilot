package daemon

import "time"

// newWatcherForTest constructs a minimal Watcher backed by a fake pipeClient.
// NewWatcher's production signature is preserved; this helper is excluded
// from the production binary by living in a *_test.go file.
func newWatcherForTest(name string, fc pipeClient) *Watcher {
	return &Watcher{
		name:       name,
		status:     "active",
		lastStatus: "unknown",
		createdAt:  time.Now(),
		reqCh:      make(chan pipeRequest, 16),
		pauseCh:    make(chan chan struct{}),
		client:     fc,
	}
}
