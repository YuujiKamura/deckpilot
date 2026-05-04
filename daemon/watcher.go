package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"sync"
	"syscall"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

// pipeClient is the pipe I/O contract for a Ghostty session, abstracted
// from *pipe.Client so watcher logic can be exercised under a fake in
// unit tests. Kept unexported because no caller outside the daemon
// package consumes it directly — handleSend reaches the client via the
// (*Watcher).Client() accessor which returns the concrete *pipe.Client.
type pipeClient interface {
	SendKeys(text string) (uint32, error)
	SendRaw(data []byte) (uint32, error)
	WaitForAck(cmdID uint32) error
	Tail(lines int) (string, error)
	History() (string, error)
	Ping() error
	Close() error
}

// BufferNotification moved to daemon/types.go.

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
	pid         int
	hwnd        syscall.Handle
	pipePath    string
	sessionFile string
	lastHash    string
	stableCount int
	status      string
	lastStatus  string // previous status for transition detection
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
	client      pipeClient
}

// NewWatcher creates a Watcher for the given session.
func NewWatcher(name, pipePath, sessionFile string, pid int, hwndStr string, onNotify func(BufferNotification)) *Watcher {
	return &Watcher{
		name:        name,
		pid:         pid,
		hwnd:        ParseHWND(hwndStr),
		pipePath:    pipePath,
		sessionFile: sessionFile,
		status:      "active",
		lastStatus:  "unknown", // initial state for transition detection
		createdAt:   time.Now(),
		onNotify:    onNotify,
		reqCh:       make(chan pipeRequest, 16),
		pauseCh:     make(chan chan struct{}),
		client:      pipe.NewClient(pipePath),
	}
}

// Run is the single goroutine that owns all pipe I/O for this session.
// Poll on ticker, drain requests between polls.
func (w *Watcher) Run(ctx context.Context) {
	log.Printf("watcher[%s]: goroutine started, pipe=%s, hwnd=%v", w.name, w.pipePath, w.hwnd)
	defer w.client.Close()

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
			currentStatus := w.Status()
			if currentStatus == "dead" {
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
		cmdID, err := w.client.SendKeys(req.payload)
		// Wait for Ghostty to process INPUT before sending RAW_INPUT \r
		if err == nil {
			if cmdID > 0 {
				if errPoll := w.client.WaitForAck(cmdID); errPoll != nil {
					log.Printf("watcher[%s]: warning: %v", w.name, errPoll)
				}
			}
			_, err = w.client.SendRaw([]byte("\r"))
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
		res.content, res.err = w.client.Tail(req.lines)
		if res.err == nil {
			w.updateContent(res.content)
		}
		w.showCount++
	case "history":
		res.content, res.err = w.client.History()
		w.showCount++
	}
	req.result <- res
}

// tailErrorClassification carries the decision tree for how poll() should
// react to a Tail() error. Unit-tested separately so that BUSY / NO_TABS /
// transport-error policies cannot regress silently.
type tailErrorClassification struct {
	// AdvanceRetry feeds the deadRetries counter; reaching the threshold
	// promotes the session to dead. Use only for genuinely unhealthy errors.
	AdvanceRetry bool
	// MarkDead is set only for session-fatal signals (NO_TABS) — fatal here
	// means deadRetries is bypassed entirely.
	MarkDead bool
	// NewStatus is an optional status override for this poll cycle. Empty
	// means leave the existing status alone (subject to updateContent).
	NewStatus string
}

func classifyTailError(err error) tailErrorClassification {
	if err == nil {
		return tailErrorClassification{}
	}
	msg := err.Error()
	// NO_TABS is session-fatal and dominates over other classifications.
	// Use the token-boundary check rather than substring match so that
	// diagnostic strings merely *containing* NO_TABS as a word fragment
	// (e.g. "NO_TABS_ALLOWED") do not falsely kill live sessions.
	if pipe.IsNoTabs(msg) {
		return tailErrorClassification{MarkDead: true}
	}
	if pipe.IsBusy(msg) {
		// Ghostty backpressure — transient, do not erode dead-retry budget.
		return tailErrorClassification{NewStatus: "busy"}
	}
	return tailErrorClassification{AdvanceRetry: true}
}

func (w *Watcher) poll() {
	// 1. Check OS-level hang FIRST (independent of pipe health)
	isHung := IsHungAppWindow(w.hwnd)
	if isHung {
		w.mu.Lock()
		prevStatus := w.lastStatus
		// PULL BACK: If we were dead but now the OS says we're hung,
		// it means the process is still there, just not responding.
		w.status = "stalled"
		statusChanged := w.status != prevStatus
		w.mu.Unlock()

		if statusChanged && w.onNotify != nil {
			w.onNotify(BufferNotification{
				SessionName:   w.name,
				Status:        "stalled",
				StatusChanged: true,
				PrevStatus:    prevStatus,
			})
			w.mu.Lock()
			w.lastStatus = "stalled"
			w.mu.Unlock()
		}
		// If hung, don't let it die yet.
	}

	content, err := w.client.Tail(50)
	if err != nil {
		errStr := err.Error()
		c := classifyTailError(err)

		if c.MarkDead {
			w.mu.Lock()
			if w.status != "stalled" {
				w.status = "dead"
			}
			w.lastError = errStr
			w.deadAt = time.Now()
			w.mu.Unlock()
			log.Printf("watcher[%s]: NO_TABS — session ended, marking dead", w.name)
			return
		}

		if c.NewStatus == "busy" {
			// Backpressure from Ghostty (e.g. BUSY|renderer_locked). Transient
			// — do not advance dead-retry budget. Surface the condition via
			// status so handleShow can label the cached buffer as stale.
			w.mu.Lock()
			if w.status != "stalled" && w.status != "dead" {
				w.status = "busy"
			}
			w.lastError = errStr
			w.pollFail++
			w.mu.Unlock()
			log.Printf("watcher[%s]: pipe.Tail backpressure (status=busy): %v", w.name, err)
			return
		}

		// Default branch (c.AdvanceRetry): genuine transport-level errors.
		// Retry up to 3 times before promoting to dead.
		w.mu.Lock()
		w.deadRetries++
		w.pollFail++
		retries := w.deadRetries
		w.mu.Unlock()
		log.Printf("watcher[%s]: pipe.Tail error (%d/3): %v", w.name, retries, err)
		if retries >= 3 {
			w.mu.Lock()
			if w.status != "stalled" {
				w.status = "dead"
			}
			w.lastError = errStr
			w.deadAt = time.Now()
			w.mu.Unlock()
			log.Printf("watcher[%s]: marked dead after 3 consecutive failures. last error: %v", w.name, err)
		}
		return
	}
	// Reset retry counter on success
	w.mu.Lock()
	deadRetries := w.deadRetries
	w.deadRetries = 0
	w.mu.Unlock()
	if deadRetries > 0 {
		log.Printf("watcher[%s]: recovered from pipe.Tail error", w.name)
	}
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
	if err := w.client.Ping(); err != nil {
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
	prevStatus := w.lastStatus

	// Only update status if not already marked as stalled by poll()
	if w.status != "stalled" {
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
	} else {
		// Recovery: if we were stalled, but IsHung check in poll() didn't 
		// find a hang this time, we should recover.
		if !IsHungAppWindow(w.hwnd) {
			w.status = "active" // Recover to active on any update
		}
	}

	// Check for status transition
	statusChanged := w.status != prevStatus

	if w.onNotify != nil {
		w.onNotify(BufferNotification{
			SessionName:   w.name,
			Content:       content,
			Hash:          hash,
			Changed:       changed,
			StableFor:     w.stableCount,
			Status:        w.status,
			StatusChanged: statusChanged,
			PrevStatus:    prevStatus,
		})
	}

	// Update lastStatus for next iteration
	if statusChanged {
		w.lastStatus = w.status
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

// ErrPausePollingTimeout is returned by PausePolling when the watcher
// goroutine does not pick up the pause request within the supplied timeout.
// This typically means the Run() goroutine has exited (dead session) or
// is itself stuck — callers should treat this as "pause unavailable" and
// proceed without pausing rather than blocking indefinitely.
var ErrPausePollingTimeout = errors.New("watcher pause polling timed out")

// PausePolling stops the watcher from polling until the returned channel is
// closed. Use this when sending to the pipe from outside the watcher
// goroutine. If the runner is not listening on pauseCh within `timeout`,
// returns ErrPausePollingTimeout — caller should fall through. A zero
// timeout makes the operation a non-blocking probe.
func (w *Watcher) PausePolling(timeout time.Duration) (chan struct{}, error) {
	resumeCh := make(chan struct{})
	if timeout <= 0 {
		select {
		case w.pauseCh <- resumeCh:
			return resumeCh, nil
		default:
			return nil, ErrPausePollingTimeout
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case w.pauseCh <- resumeCh:
		return resumeCh, nil
	case <-timer.C:
		return nil, ErrPausePollingTimeout
	}
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

// Client returns the concrete pipe client used by this watcher. Kept as
// *pipe.Client (not the unexported pipeClient interface) so external
// callers do not see an unexported type — Go vet flags that pattern.
// The watcher itself uses the interface field internally for testability.
func (w *Watcher) Client() *pipe.Client {
	if c, ok := w.client.(*pipe.Client); ok {
		return c
	}
	// Tests inject a fakeClient via newWatcherForTest. Those tests must
	// not call Client(); reaching here means a test misuse.
	panic("Watcher.Client() called on test-injected fake; use w.client directly in tests")
}

// WatcherProfile holds session profiling counters.
type WatcherProfile struct {
	CreatedAt   time.Time
	PID         int
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
		PID:         w.pid,
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
