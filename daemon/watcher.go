package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

var (
	// Quota regex: 5h:86%(resets 1pm) wk:41%(resets Mon) sn:9%(resets May 1)
	quotaRegex = regexp.MustCompile(`5h:(\d+)%\(([^)]+)\)\s+wk:(\d+)%\(([^)]+)\)\s+sn:(\d+)%\(([^)]+)\)`)

	// Model regexes
	haikuRegex  = regexp.MustCompile(`Haiku 4\.5 with high effort`)
	sonnetRegex = regexp.MustCompile(`Sonnet 4\.6`)
	opusRegex   = regexp.MustCompile(`Opus 4\.7`)
	geminiRegex = regexp.MustCompile(`Gemini 2\.0 Flash`) // assuming a banner
	codexRegex  = regexp.MustCompile(`OpenAI Codex`)     // assuming a banner
)

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
	mu            sync.Mutex
	name          string
	pid           int
	hwnd          syscall.Handle
	pipePath      string
	sessionFile   string
	lastHash      string
	stableCount   int
	status        string
	lastStatus    string // previous status for transition detection
	lastContent   string
	lastError     string
	deadAt        time.Time
	deadRetries   int
	createdAt     time.Time
	sendCount     int
	showCount     int
	pollSuccess   int
	pollFail      int
	lastPollOK    time.Time
	lastChangedAt time.Time // when the output buffer last changed content
	onNotify      func(BufferNotification)
	onReport      func(string, map[string]string) // session name, tags
	last5h        int                             // last seen 5h quota %
	model         string                          // auto-detected model
	reqCh         chan pipeRequest
	pauseCh       chan chan struct{} // send pause signal, receive resume signal
}

// NewWatcher creates a Watcher for the given session.
func NewWatcher(name, pipePath, sessionFile string, pid int, hwndStr string, onNotify func(BufferNotification), onReport func(string, map[string]string)) *Watcher {
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
		onReport:    onReport,
		reqCh:       make(chan pipeRequest, 16),
		pauseCh:     make(chan chan struct{}),
	}
}

// Run is the single goroutine that owns all pipe I/O for this session.
// Poll on ticker, drain requests between polls.
func (w *Watcher) Run(ctx context.Context) {
	log.Printf("watcher[%s]: goroutine started, pipe=%s, hwnd=%v", w.name, w.pipePath, w.hwnd)
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

	content, err := pipe.Tail(w.pipePath, 50)
	if err != nil {
		errStr := err.Error()
		// NO_TABS = terminal has no tabs (session ended). Immediate dead, no retry.
		if strings.Contains(errStr, "NO_TABS") {
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
		// Other errors: retry 3 times before dead
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
	prevStatus := w.lastStatus

	// Auto-detection and scraping
	if changed && content != "" {
		w.scrapeMetadata(content)
	}

	// Only update status if not already marked as stalled by poll()
	if w.status != "stalled" {
		if changed {
			w.lastHash = hash
			w.stableCount = 0
			w.status = "active"
			w.lastChangedAt = time.Now()
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
	CreatedAt     time.Time
	PID           int
	SendCount     int
	ShowCount     int
	PollSuccess   int
	PollFail      int
	LastPollOK    time.Time
	LastChangedAt time.Time // when the output buffer last changed
	DeadAt        time.Time
	LastError     string
}

// Profile returns a snapshot of this watcher's profiling counters.
func (w *Watcher) Profile() WatcherProfile {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WatcherProfile{
		CreatedAt:     w.createdAt,
		PID:           w.pid,
		SendCount:     w.sendCount,
		ShowCount:     w.showCount,
		PollSuccess:   w.pollSuccess,
		PollFail:      w.pollFail,
		LastPollOK:    w.lastPollOK,
		LastChangedAt: w.lastChangedAt,
		DeadAt:        w.deadAt,
		LastError:     w.lastError,
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

func (w *Watcher) scrapeMetadata(content string) {
	// 1. Model detection (priority: Haiku > Sonnet > Opus > Gemini > Codex)
	detectedModel := ""
	if haikuRegex.MatchString(content) {
		detectedModel = "haiku"
	} else if sonnetRegex.MatchString(content) {
		detectedModel = "sonnet"
	} else if opusRegex.MatchString(content) {
		detectedModel = "opus"
	} else if geminiRegex.MatchString(content) {
		detectedModel = "gemini"
	} else if codexRegex.MatchString(content) {
		detectedModel = "codex"
	}

	if detectedModel != "" && detectedModel != w.model {
		w.model = detectedModel
		if w.onReport != nil {
			w.onReport(w.name, map[string]string{"model": detectedModel, "auto": "true"})
		}
	}

	// 2. Quota scraping
	matches := quotaRegex.FindAllStringSubmatch(content, -1)
	if len(matches) > 0 {
		// Take the last one (most recent)
		m := matches[len(matches)-1]
		if len(m) >= 7 {
			quotaStr := fmt.Sprintf("5h:%s%% wk:%s%% sn:%s%%", m[1], m[3], m[5])
			resetETA := m[2]
			val5h, _ := strconv.Atoi(m[1])

			if w.onReport != nil {
				w.onReport(w.name, map[string]string{"quota": quotaStr, "reset": resetETA, "5h": m[1]})
			}

			// Alert on 90% threshold
			if val5h >= 90 && w.last5h < 90 {
				w.notifyThreshold(val5h)
			}
			w.last5h = val5h
		}
	}
}

func (w *Watcher) notifyThreshold(val int) {
	msg := fmt.Sprintf("quota warn: %s 5h=%d%%", w.name, val)
	log.Printf("ALERT: %s", msg)
	if w.onNotify != nil {
		w.onNotify(BufferNotification{
			SessionName:   w.name,
			Status:        w.status,
			StatusChanged: false,
			Content:       msg, // special message for alert
		})
	}
}
