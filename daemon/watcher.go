package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

// BufferNotification is emitted when the watcher detects a change or stability.
type BufferNotification struct {
	SessionName string
	Content     string
	Hash        string
	Changed     bool
	StableFor   int
	Status      string
}

// pipeRequest is a command queued to the watcher's pipe goroutine.
type pipeRequest struct {
	kind    string // "sendkeys", "tail", "history"
	payload string // text for sendkeys, unused for tail/history
	lines   int    // for tail
	result  chan pipeResult
}

type pipeResult struct {
	content string
	err     error
}

// Watcher monitors a single Ghostty session. All pipe I/O for this session
// goes through the watcher's single goroutine to avoid contention.
type Watcher struct {
	mu          sync.Mutex
	name        string
	pipePath    string
	sessionFile string
	lastHash    string
	stableCount int
	status      string
	lastContent string
	lastError   string
	deadAt      time.Time
	deadRetries int
	createdAt   time.Time
	sendCount   int
	showCount   int
	pollSuccess int
	pollFail    int
	lastPollOK  time.Time
	onNotify    func(BufferNotification)
	reqCh       chan pipeRequest
	pauseCh     chan chan struct{} // send pause signal, receive resume signal
}

// NewWatcher creates a Watcher for the given session.
func NewWatcher(name, pipePath, sessionFile string, onNotify func(BufferNotification)) *Watcher {
	return &Watcher{
		name:        name,
		pipePath:    pipePath,
		sessionFile: sessionFile,
		status:      "active",
		createdAt:   time.Now(),
		onNotify:    onNotify,
		reqCh:       make(chan pipeRequest, 16),
		pauseCh:     make(chan chan struct{}),
	}
}

// Run is the single goroutine that owns all pipe I/O for this session.
// Poll on ticker, drain requests between polls.
func (w *Watcher) Run(ctx context.Context) {
	log.Printf("watcher[%s]: goroutine started, pipe=%s", w.name, w.pipePath)
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Drain all pending requests first (commands take priority over polling)
		drained := true
		for drained {
			select {
			case req := <-w.reqCh:
				w.handleRequest(req)
			default:
				drained = false
			}
		}

		select {
		case <-ctx.Done():
			w.logProfile()
			return
		case req := <-w.reqCh:
			w.handleRequest(req)
		case resumeCh := <-w.pauseCh:
			// Pause polling until resume signal
			<-resumeCh
		case <-ticker.C:
			w.poll()
			if w.Status() == "dead" {
				w.mu.Lock()
				errMsg := w.lastError
				deadTime := w.deadAt
				w.mu.Unlock()
				log.Printf("watcher: session %q dead at %s, error: %s, stopping goroutine", w.name, deadTime.Format("15:04:05"), errMsg)
				w.logProfile()
				return
			}
		}
	}
}

func (w *Watcher) handleRequest(req pipeRequest) {
	var res pipeResult
	switch req.kind {
	case "sendkeys":
		prevStatus := w.Status()
		// Send text via INPUT
		err := pipe.SendKeys(w.pipePath, req.payload)
		// Wait for Ghostty to process INPUT before sending RAW_INPUT \r
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			err = pipe.SendRaw(w.pipePath, []byte("\r"))
		}
		res.err = err
		if err == nil {
			// Wait a moment for Ghostty to process, then poll
			time.Sleep(200 * time.Millisecond)
			w.poll()
			newStatus := w.Status()
			if newStatus != prevStatus {
				res.content = fmt.Sprintf("submitted (status: %s → %s)", prevStatus, newStatus)
			} else {
				res.content = fmt.Sprintf("sent (status: %s, no change yet)", newStatus)
			}
		}
		w.sendCount++
	case "tail":
		res.content, res.err = pipe.Tail(w.pipePath, req.lines)
		if res.err == nil {
			w.updateContent(res.content)
		}
		w.showCount++
	case "history":
		res.content, res.err = pipe.History(w.pipePath)
		w.showCount++
	}
	req.result <- res
}

func (w *Watcher) poll() {
	content, err := pipe.Tail(w.pipePath, 50)
	if err != nil {
		errStr := err.Error()
		// NO_TABS = terminal has no tabs (session ended). Immediate dead, no retry.
		if strings.Contains(errStr, "NO_TABS") {
			w.mu.Lock()
			w.status = "dead"
			w.lastError = errStr
			w.deadAt = time.Now()
			w.mu.Unlock()
			log.Printf("watcher[%s]: NO_TABS — session ended, marking dead immediately", w.name)
			return
		}
		// Other errors: retry 3 times before dead
		w.mu.Lock()
		w.deadRetries++
		w.pollFail++
		retries := w.deadRetries
		w.mu.Unlock()
		log.Printf("watcher[%s]: pipe.Tail error (%d/3): %v", w.name, retries, err)
		if retries >= 3 {
			w.mu.Lock()
			w.status = "dead"
			w.lastError = errStr
			w.deadAt = time.Now()
			w.mu.Unlock()
			log.Printf("watcher[%s]: marked dead after 3 consecutive failures. last error: %v", w.name, err)
		}
		return
	}
	// Reset retry counter on success
	w.mu.Lock()
	w.deadRetries = 0
	w.mu.Unlock()
	w.pollSuccess++
	w.lastPollOK = time.Now()
	w.updateContent(content)
}

// Revive attempts to bring a dead watcher back to life by pinging the pipe.
// Returns true if the watcher was successfully revived.
func (w *Watcher) Revive() bool {
	if w.Status() != "dead" {
		return true // already alive
	}
	// Try to ping the pipe
	if err := pipe.Ping(w.pipePath); err != nil {
		return false
	}
	// Pipe is responsive again — reset status and poll
	w.mu.Lock()
	w.status = "active"
	w.stableCount = 0
	w.mu.Unlock()
	w.poll()
	return w.Status() != "dead"
}

func (w *Watcher) updateContent(content string) {
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

// Send queues a send+enter to this session's pipe goroutine.
// Returns status feedback (e.g. "submitted (status: idle → active)").
func (w *Watcher) Send(text string) (string, error) {
	if w.Status() == "dead" {
		return "", fmt.Errorf("session %s is dead", w.name)
	}
	req := pipeRequest{
		kind:    "sendkeys",
		payload: text,
		result:  make(chan pipeResult, 1),
	}
	select {
	case w.reqCh <- req:
	default:
		return "", fmt.Errorf("session %s request queue full", w.name)
	}
	res := <-req.result
	return res.content, res.err
}

// FreshTail queues a tail request to the pipe goroutine.
func (w *Watcher) FreshTail(lines int) (string, error) {
	if w.Status() == "dead" {
		return w.LastContent(), nil // return cached content
	}
	req := pipeRequest{
		kind:   "tail",
		lines:  lines,
		result: make(chan pipeResult, 1),
	}
	select {
	case w.reqCh <- req:
	default:
		return w.LastContent(), nil
	}
	res := <-req.result
	return res.content, res.err
}

// FreshHistory queues a history request to the pipe goroutine.
func (w *Watcher) FreshHistory() (string, error) {
	if w.Status() == "dead" {
		return "", fmt.Errorf("session %s is dead", w.name)
	}
	req := pipeRequest{
		kind:   "history",
		result: make(chan pipeResult, 1),
	}
	select {
	case w.reqCh <- req:
	default:
		return "", fmt.Errorf("session %s request queue full", w.name)
	}
	res := <-req.result
	return res.content, res.err
}

// PausePolling stops the watcher from polling until the returned channel is closed.
// Use this when sending to the pipe from outside the watcher goroutine.
func (w *Watcher) PausePolling() chan struct{} {
	resumeCh := make(chan struct{})
	w.pauseCh <- resumeCh
	return resumeCh
}

// Status returns the current status.
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

// WatcherProfile holds session profiling counters.
type WatcherProfile struct {
	CreatedAt   time.Time
	SendCount   int
	ShowCount   int
	PollSuccess int
	PollFail    int
	LastPollOK  time.Time
	DeadAt      time.Time
	LastError   string
}

// Profile returns a snapshot of this watcher's profiling counters.
func (w *Watcher) Profile() WatcherProfile {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WatcherProfile{
		CreatedAt:   w.createdAt,
		SendCount:   w.sendCount,
		ShowCount:   w.showCount,
		PollSuccess: w.pollSuccess,
		PollFail:    w.pollFail,
		LastPollOK:  w.lastPollOK,
		DeadAt:      w.deadAt,
		LastError:   w.lastError,
	}
}

func (w *Watcher) logProfile() {
	p := w.Profile()
	uptime := time.Since(p.CreatedAt).Round(time.Second)
	lastOK := "never"
	if !p.LastPollOK.IsZero() {
		lastOK = p.LastPollOK.Format("15:04:05")
	}
	deadInfo := ""
	if !p.DeadAt.IsZero() {
		deadInfo = fmt.Sprintf(", dead=%s err=%q", p.DeadAt.Format("15:04:05"), p.LastError)
	}
	log.Printf("profile[%s]: uptime=%s send=%d show=%d poll_ok=%d poll_fail=%d lastPollOK=%s%s",
		w.name, uptime, p.SendCount, p.ShowCount, p.PollSuccess, p.PollFail, lastOK, deadInfo)
}
