package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/YuujiKamura/deckpilot/pipe"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Domain / value types (IdleHook, sessionInfo, BufferNotification) live in
// daemon/types.go. This file keeps daemon lifecycle + orchestration.

// IPCPipePath returns the named pipe path for the daemon.
func IPCPipePath() string {
	if suffix := os.Getenv("DECKPILOT_PIPE_SUFFIX"); suffix != "" {
		return `\\.\pipe\deckpilot-daemon-` + suffix
	}
	return `\\.\pipe\deckpilot-daemon`
}

// Daemon manages Ghostty sessions and handles IPC from the CLI.
type Daemon struct {
	mu          sync.Mutex
	sessions    map[string]string   // session name -> pipe path
	wsURLs      map[string]string   // session name -> WebSocket URL (for web sessions)
	appRuntimes map[string]string   // session name -> app runtime (winui3/win32/web)
	watchers    map[string]*Watcher // session name -> watcher
	lastNotify  map[string]BufferNotification
	onNotify    func(BufferNotification) // external callback (optional)
	lastUsed    map[string]string        // caller -> session name
	idleHooks   []IdleHook               // hooks to execute on idle transition
	WSPort      int                      // WebSocket control port (default 8080)

	lastDiscoveryTick int64 // unix nano; atomic load/store only. discoveryLoop liveness indicator read by selfWatchdog.
}

// New creates a new Daemon instance. Idle hooks persisted under
// ~/.deckpilot/idle-hooks/ are restored into the in-memory slice so
// `deckpilot notify add ...` survives daemon restarts (issue #31).
// Load failures are non-fatal: a missing directory returns an empty
// slice, and individual unparseable files are skipped with a warning.
func New() *Daemon {
	d := &Daemon{
		sessions:    make(map[string]string),
		wsURLs:      make(map[string]string),
		appRuntimes: make(map[string]string),
		watchers:    make(map[string]*Watcher),
		lastNotify:  make(map[string]BufferNotification),
		lastUsed:    make(map[string]string),
		idleHooks:   make([]IdleHook, 0),
		WSPort:      8080, // Default port
	}
	if loaded := loadIdleHooks(); len(loaded) > 0 {
		d.idleHooks = loaded
		log.Printf("daemon: restored %d idle hook(s) from %s", len(loaded), idleHooksDir())
	}
	return d
}

// Run starts the daemon: handles single-instance takeover, starts background
// discovery, and listens for IPC connections on the daemon pipe.
func (d *Daemon) Run() error {
	// 1. SINGLETON MUTEX FIRST: claim the singleton invariant before doing
	// anything that touches an existing daemon. The previous order
	// (takeover first, mutex second) was a "last instance wins" design:
	// a double-launch would SHUTDOWN the healthy daemon and then itself
	// die on a still-held mutex, leaving zero daemons (observed
	// 2026-06-04 during initial smoke). The correct invariant is "first
	// instance wins": if the mutex is already held, a healthy peer
	// exists, and we must exit without disturbing its pipe.
	mutexHandle, mutexExists, mutexErr := acquireSingletonMutex()
	if mutexErr != nil {
		return fmt.Errorf("acquire singleton mutex: %w", mutexErr)
	}
	if mutexExists {
		releaseSingletonMutex(mutexHandle)
		return fmt.Errorf("daemon: another instance is already running (singleton mutex %s is held)", singletonMutexName())
	}
	defer releaseSingletonMutex(mutexHandle)

	// 2. GHOST CLEANUP: we hold the mutex, so anything still bound to the
	// pipe is a ghost — an older build with no mutex awareness, or a
	// daemon whose handle was lost. SHUTDOWN it so we can rebind. A
	// living mutex-aware peer was already filtered out above; this can
	// only kill ghosts.
	pipePath := IPCPipePath()
	conn, err := winio.DialPipe(pipePath, nil)
	if err == nil {
		log.Printf("daemon: ghost daemon detected at %s (no mutex), requesting shutdown", pipePath)
		fmt.Fprintln(conn, "SHUTDOWN")
		conn.Close()

		// Wait for the pipe to be released (up to 3 seconds)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			c, err := winio.DialPipe(pipePath, nil)
			if err != nil {
				break
			}
			c.Close()
			time.Sleep(200 * time.Millisecond)
		}
	}

	// 2. Set up logging and core initialization.
	// lumberjack handles rotation in-process: O_APPEND alone grew the
	// log to ~491 MB after a few days. Bound the on-disk footprint to
	// roughly 30 MB live + 3 compressed backups, expiring after a week.
	// We keep the startup banner so each rotated file has a clear
	// pid/timestamp anchor for postmortem grepping.
	logPath := filepath.Join(os.TempDir(), "deckpilot-daemon.log")
	log.SetOutput(&lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    10, // megabytes per file before rotation
		MaxBackups: 3,
		MaxAge:     7, // days
		Compress:   true,
	})
	log.Printf("=== daemon started pid=%d ===", os.Getpid())

	// 3. Start background discovery and refresh loop.
	// Using a channel to feed discovered sessions avoids blocking startup.
	discoveryCh := make(chan []pipe.Session, 1)
	// Seed the liveness tick from startup so selfWatchdog has an implicit
	// grace period of watchdogTimeout: the daemon is not judged hung until
	// that long has elapsed without discovery progressing.
	d.markDiscoveryTick()
	go d.discoveryLoop(discoveryCh)

	// Internal self-watchdog: if discovery stops progressing for
	// watchdogTimeout, exit so the next `deckpilot` invocation can spawn a
	// fresh daemon (a hung daemon would otherwise hold the singleton mutex
	// and pipe and block respawn). The stop channel is never closed in
	// production — the daemon only ends via os.Exit — so this is a single
	// long-lived goroutine, not a leak.
	watchdogStop := make(chan struct{})
	go d.selfWatchdog(watchdogInterval, watchdogTimeout, os.Exit, watchdogStop)

	go func() {
		for sessions := range discoveryCh {
			for _, s := range sessions {
				d.addSession(s.Name, s.PipePath, s.WsURL, s.SessionFile, s.PID, s.HWND, s.AppRuntime)
			}
		}
	}()

	// 4. Start WebSocket bridge for GitHub Pages/Web UI
	if d.WSPort > 0 {
		go func() {
			addr := fmt.Sprintf("127.0.0.1:%d", d.WSPort)
			if err := d.ServeWS(addr); err != nil {
				log.Printf("daemon: WebSocket server error: %v", err)
			}
		}()
	}

	listener, err := winio.ListenPipe(pipePath, nil)
	if err != nil {
		if strings.Contains(err.Error(), "Access is denied") || strings.Contains(err.Error(), "0x5") {
			return fmt.Errorf("daemon is already running and refusing to exit (pipe %s locked)", pipePath)
		}
		return fmt.Errorf("listen pipe: %w", err)
	}
	defer listener.Close()
	log.Printf("daemon: listening on %s", pipePath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)
			continue
		}
		go d.handleConn(conn)
	}
}

// discoveryLoop runs in a background goroutine, performing initial and periodic
// discovery/refresh. Results are sent through a channel to be processed by the
// main daemon state. Interval is 10s to minimize CPU overhead.
func (d *Daemon) discoveryLoop(ch chan []pipe.Session) {
	// Initial discovery
	if sessions, err := pipe.Discover(); err == nil {
		ch <- sessions
		log.Printf("daemon: initial discovery found %d session(s)", len(sessions))
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Record that the loop entered another iteration before doing any
		// work, so selfWatchdog's liveness signal is independent of how long
		// refreshSessions/Discover take this round.
		d.markDiscoveryTick()

		// Perform liveness checks and pruning
		d.refreshSessions()

		// Re-discover new sessions
		if sessions, err := pipe.Discover(); err == nil {
			ch <- sessions
		}
	}
}

// markDiscoveryTick records that discoveryLoop just entered another iteration.
// selfWatchdog reads this (via lastDiscoveryTick) to decide whether the daemon
// is wedged. Atomic store keeps it race-free against the watchdog's load.
func (d *Daemon) markDiscoveryTick() {
	atomic.StoreInt64(&d.lastDiscoveryTick, time.Now().UnixNano())
}

// addSession registers a session and starts a watcher goroutine.
func (d *Daemon) addSession(name, pipePath, wsURL, sessionFile string, pid int, hwnd string, appRuntime string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.sessions[name]; exists {
		return
	}
	d.sessions[name] = pipePath
	if wsURL != "" {
		d.wsURLs[name] = wsURL
	}
	d.appRuntimes[name] = appRuntime

	w := NewWatcher(name, pipePath, sessionFile, pid, hwnd, func(n BufferNotification) {
		d.mu.Lock()
		d.lastNotify[name] = n
		d.mu.Unlock()

		// Execute hooks on status transitions
		if n.StatusChanged {
			// Trigger on: active → idle (task complete)
			// Trigger on: any → stalled (hang detected)
			if (n.PrevStatus == "active" && n.Status == "idle") || n.Status == "stalled" {
				d.executeStatusHooks(n)
			}
		}

		if d.onNotify != nil {
			d.onNotify(n)
		}
	})
	d.watchers[name] = w
	go w.Run(context.Background())
	log.Printf("daemon: added session %q -> %s", name, pipePath)
}

func (d *Daemon) setLastUsed(caller, session string) {
	if caller == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastUsed[caller] = session
}

func (d *Daemon) getLastUsed(caller string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.lastUsed[caller]
	return s, ok
}

// resolvePipePath looks up the pipe path for a session name.
func (d *Daemon) resolvePipePath(name string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.sessions[name]
	return p, ok
}

// resolveWsURL looks up the WebSocket URL for a session name.
func (d *Daemon) resolveWsURL(name string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	u, ok := d.wsURLs[name]
	return u, ok
}

// getWatcher returns the watcher for a session.
func (d *Daemon) getWatcher(name string) (*Watcher, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.watchers[name]
	return w, ok
}

// listSessions returns a snapshot of all session info.
func (d *Daemon) listSessions() []sessionInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	var result []sessionInfo
	for name, pipePath := range d.sessions {
		status := "unknown"
		uptime := ""
		pid := 0
		if w, ok := d.watchers[name]; ok {
			status = w.Status()
			p := w.Profile()
			uptime = formatUptime(time.Since(p.CreatedAt))
			pid = p.PID
		}
		result = append(result, sessionInfo{
			Name:       name,
			PID:        pid,
			PipePath:   pipePath,
			WsURL:      d.wsURLs[name],
			AppRuntime: d.appRuntimes[name],
			Status:     status,
			Uptime:     uptime,
		})
	}
	return result
}

func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// refreshSessions re-discovers sessions, attempts to revive dead watchers, and prunes sessions whose processes are gone.
func (d *Daemon) refreshSessions() {
	d.mu.Lock()

	// 1. Identify sessions to prune (status="dead" and process gone)
	var toPrune []string
	for name, w := range d.watchers {
		if w.Status() == "dead" {
			p := w.Profile()
			// Only prune if we have a real PID and it's confirmed gone.
			// PID 0 is used for mock sessions in tests and should not be pruned.
			if p.PID > 0 && !pipe.IsProcessAlive(p.PID) {
				toPrune = append(toPrune, name)
			}
		}
	}

	// 2. Perform pruning
	for _, name := range toPrune {
		delete(d.sessions, name)
		delete(d.wsURLs, name)
		delete(d.appRuntimes, name)
		delete(d.watchers, name)
		delete(d.lastNotify, name)
		log.Printf("daemon: pruned dead session %q (process gone)", name)
	}

	// 3. Identify dead sessions to attempt revival
	var toRevive []string
	for name, w := range d.watchers {
		if w.Status() == "dead" {
			toRevive = append(toRevive, name)
		}
	}
	d.mu.Unlock()

	// 4. Attempt revival outside the main lock
	for _, name := range toRevive {
		w, ok := d.getWatcher(name)
		if !ok {
			continue
		}
		if w.Revive() {
			log.Printf("daemon: revived session %q", name)
			go w.Run(context.Background())
		}
	}
}

// EnsureRunning tries to PING the daemon. If it fails, starts the daemon
// as a detached subprocess and waits up to 3s for it to respond.
func EnsureRunning() error {
	if pingDaemon() {
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
	}

	cmd := exec.Command(exe, "daemon")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// Detached process — don't wait, don't hold reference
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pingDaemon() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start within 3s")
}

func pingDaemon() bool {
	conn, err := winio.DialPipe(IPCPipePath(), nil)
	if err != nil {
		return false
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(1 * time.Second))
	if _, err := conn.Write([]byte("PING\n")); err != nil {
		return false
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}
	return string(buf[:n]) == "PONG\n"
}

// AddIdleHook adds a hook to be executed when sessions become idle.
// Generates an ID if the caller didn't supply one and best-effort
// persists the hook to ~/.deckpilot/idle-hooks/<id>.json so it
// survives daemon restarts (issue #31). Persistence failures are
// logged but never block the in-memory append: if the disk is
// read-only the hook still fires for the lifetime of this daemon.
//
// Order: in-memory append happens first, then file write. If the
// write fails the in-memory state is the source of truth — there is
// no rollback. This matches the wider daemon contract that hooks live
// in d.idleHooks at fire time.
func (d *Daemon) AddIdleHook(hook IdleHook) {
	if hook.ID == "" {
		hook.ID = generateHookID()
	}
	d.mu.Lock()
	d.idleHooks = append(d.idleHooks, hook)
	d.mu.Unlock()
	if err := writeIdleHookFile(hook); err != nil {
		log.Printf("daemon: idle-hook persist warning (continuing in-memory): %v", err)
	}
	log.Printf("daemon: added idle hook id=%s type=%s", hook.ID, hook.Type)
}

// RemoveIdleHooks removes all idle hooks from memory and disk.
// In-memory clear happens first; the file sweep is best-effort and
// logs per-file failures without surfacing them — the IPC contract
// only promises that the in-memory state has been wiped.
func (d *Daemon) RemoveIdleHooks() {
	d.mu.Lock()
	d.idleHooks = make([]IdleHook, 0)
	d.mu.Unlock()
	removeAllIdleHookFiles()
	log.Printf("daemon: removed all idle hooks (memory + disk)")
}

// ListIdleHooks returns a copy of current idle hooks
func (d *Daemon) ListIdleHooks() []IdleHook {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([]IdleHook, len(d.idleHooks))
	copy(result, d.idleHooks)
	return result
}

// executeStatusHooks fires hooks matching the given status notification.
//
// Collection and one-time pruning happen under a single lock pass: a OneTime
// hook is removed from d.idleHooks *before* any goroutine runs, so a second
// status transition arriving on another goroutine cannot observe the hook and
// re-fire it. This invariant is locked by TestHookInvariant_OneTimeFires
// ExactlyOnce. The previous implementation deferred removal through a 100ms
// goroutine, which allowed back-to-back notifications to double-fire.
func (d *Daemon) executeStatusHooks(notification BufferNotification) {
	d.mu.Lock()
	toFire := make([]IdleHook, 0, len(d.idleHooks))
	remaining := make([]IdleHook, 0, len(d.idleHooks))
	prunedIDs := make([]string, 0)
	for _, hook := range d.idleHooks {
		if hook.SessionFilter != "" && hook.SessionFilter != notification.SessionName {
			remaining = append(remaining, hook)
			continue
		}
		toFire = append(toFire, hook)
		if !hook.OneTime {
			remaining = append(remaining, hook)
		} else {
			// Capture IDs for sidecar-file cleanup outside the mutex.
			prunedIDs = append(prunedIDs, hook.ID)
		}
	}
	prunedOneTime := len(d.idleHooks) - len(remaining)
	d.idleHooks = remaining
	d.mu.Unlock()

	if prunedOneTime > 0 {
		log.Printf("daemon: pruned %d one-time hook(s) atomically before fire", prunedOneTime)
		// Persistence cleanup mirrors the in-memory prune. Outside the
		// mutex on purpose: file I/O must not block other IPC handlers,
		// and the in-memory state has already been updated atomically
		// so a crash between prune and unlink only leaks a stale file
		// that loadIdleHooks will resurrect on next boot — acceptable
		// trade for not extending mutex scope across disk I/O.
		for _, id := range prunedIDs {
			if id == "" {
				continue
			}
			if err := removeIdleHookFile(id); err != nil {
				log.Printf("daemon: idle-hook file cleanup warning: %v", err)
			}
		}
	}

	for _, hook := range toFire {
		go func(h IdleHook, n BufferNotification) {
			if err := d.executeHook(h, n); err != nil {
				log.Printf("daemon: hook execution error: %v", err)
			}
		}(hook, notification)
	}
}

// executeHook executes a single hook
func (d *Daemon) executeHook(hook IdleHook, notification BufferNotification) error {
	prefix := "[NOTIFY]"
	verb := "idle"
	if notification.Status == "stalled" {
		prefix = "[STALLED]"
		verb = "hung"
	}

	switch hook.Type {
	case "stdout":
		message := hook.Message
		if message == "" {
			message = fmt.Sprintf("Session %s is %s", notification.SessionName, verb)
		}
		log.Printf("STATUS_HOOK_STDOUT: %s", message)
		fmt.Printf("%s: %s\n", prefix, message)

		// Also write to file for verification
		verificationFile := filepath.Join(os.TempDir(), "deckpilot-status-verification.txt")
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		content := fmt.Sprintf("[%s] %s\n", timestamp, message)
		os.WriteFile(verificationFile, []byte(content), 0644)
		log.Printf("STATUS_HOOK_FILE: wrote to %s", verificationFile)

		return nil

	case "http":
		return d.executeHTTPHook(hook, notification)

	case "send":
		return d.executeSendHook(hook, notification)

	case "callback":
		return d.executeCallbackHook(hook, notification)

	default:
		return fmt.Errorf("unknown hook type: %s", hook.Type)
	}
}

// executeHTTPHook sends HTTP notification
func (d *Daemon) executeHTTPHook(hook IdleHook, notification BufferNotification) error {
	if hook.URL == "" {
		return fmt.Errorf("http hook missing URL")
	}

	method := hook.Method
	if method == "" {
		method = "POST"
	}

	payload := map[string]interface{}{
		"event":       "idle_transition",
		"session":     notification.SessionName,
		"status":      notification.Status,
		"prev_status": notification.PrevStatus,
		"timestamp":   time.Now().Unix(),
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("json marshal error: %w", err)
	}

	req, err := http.NewRequest(method, hook.URL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("create request error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range hook.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http request error: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("daemon: idle hook http %s %s -> %d", method, hook.URL, resp.StatusCode)
	return nil
}

// executeSendHook sends message to target session
func (d *Daemon) executeSendHook(hook IdleHook, notification BufferNotification) error {
	targetSession := hook.TargetSession
	if targetSession == "" {
		return fmt.Errorf("send hook missing target_session")
	}

	message := hook.Message
	if message == "" {
		message = fmt.Sprintf("Session %s is now idle", notification.SessionName)
	}

	watcher, ok := d.getWatcher(targetSession)
	if !ok {
		return fmt.Errorf("target session %s not found", targetSession)
	}

	_, err := watcher.Send(message)
	if err != nil {
		return fmt.Errorf("send to session %s error: %w", targetSession, err)
	}

	log.Printf("daemon: idle hook sent message to session %s", targetSession)
	return nil
}

// executeCallbackHook sends notification back to the callback session
func (d *Daemon) executeCallbackHook(hook IdleHook, notification BufferNotification) error {
	callbackSession := hook.CallbackSession
	if callbackSession == "" {
		return fmt.Errorf("callback hook missing callback_session")
	}

	prefix := "[NOTIFY]"
	verb := "idle"
	if notification.Status == "stalled" {
		prefix = "[STALLED]"
		verb = "hung"
	}

	message := fmt.Sprintf("%s %s is %s\n", prefix, notification.SessionName, verb)
	if hook.Message != "" {
		message = hook.Message + "\n"
	}

	watcher, ok := d.getWatcher(callbackSession)
	if !ok {
		return fmt.Errorf("callback session %s not found", callbackSession)
	}

	_, err := watcher.Send(message)
	if err != nil {
		return fmt.Errorf("send to callback session %s error: %w", callbackSession, err)
	}

	log.Printf("daemon: idle callback sent to session %s: %s", callbackSession, message)
	return nil
}

// (removeOneTimeHooks / hookEquals removed: one-time pruning now happens
// atomically inside executeStatusHooks, so the equality-matching removal
// helper is no longer needed.)

// snapshotSessions returns a point-in-time view of all watched sessions,
// suitable to pass into the pure policy helpers in policy.go. Taking the
// snapshot under a single lock avoids the split-read hazards that the previous
// per-call mu.Lock dance had.
func (d *Daemon) snapshotSessions() []SessionSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]SessionSnapshot, 0, len(d.watchers))
	for name, w := range d.watchers {
		p := w.Profile()
		out = append(out, SessionSnapshot{
			Name:       name,
			Status:     w.Status(),
			LastPollOK: p.LastPollOK,
		})
	}
	return out
}

// shouldRegisterCallback is a thin orchestration wrapper over the pure policy
// in ShouldRegisterCallback. It snapshots the registry, delegates the decision,
// and emits the existing log lines so operators can continue to grep for them.
func (d *Daemon) shouldRegisterCallback(targetSession, callerSession string) bool {
	log.Printf("daemon: shouldRegisterCallback target=%s caller=%s", targetSession, callerSession)

	decision := ShouldRegisterCallback(targetSession, callerSession, d.snapshotSessions())
	switch decision.Reason {
	case CallbackRejectSameRaw:
		log.Printf("daemon: skipping callback - target and caller are the same")
	case CallbackRejectUnresolved:
		log.Printf("daemon: could not resolve caller session for %s", callerSession)
	case CallbackRejectHeuristicLoop:
		log.Printf("daemon: skipping callback - resolved caller aliases to target %s (heuristic self-loop)", targetSession)
	case CallbackRejectTargetNotFound:
		log.Printf("daemon: target session %s not found", targetSession)
	case CallbackAccept:
		log.Printf("daemon: callback conditions met - target=%s caller=%s", targetSession, decision.ActualCaller)
	}
	return decision.Register
}

// resolveCallerSession is a thin orchestration wrapper over the pure
// ResolveCaller policy. Preserves previous log lines for grep-compatibility.
func (d *Daemon) resolveCallerSession(caller string) string {
	if caller == "" {
		return ""
	}
	resolved := ResolveCaller(caller, d.snapshotSessions())
	switch {
	case resolved == "":
		log.Printf("daemon: could not resolve caller %s to any session", caller)
	case resolved == caller:
		// exact-match path — no extra log, matches prior behavior
	default:
		log.Printf("daemon: resolved caller %s to recent session: %s", caller, resolved)
	}
	return resolved
}

// registerCallbackHook registers a one-time callback hook for idle notification
func (d *Daemon) registerCallbackHook(targetSession, callerSession string) {
	actualCaller := d.resolveCallerSession(callerSession)
	if actualCaller == "" {
		log.Printf("daemon: cannot register callback - caller resolution failed")
		return
	}

	hook := IdleHook{
		Type:            "callback",
		SessionFilter:   targetSession,
		CallbackSession: actualCaller,
		OneTime:         true,
		Message:         "", // will use default message
	}

	d.AddIdleHook(hook)
	log.Printf("daemon: auto-registered callback hook %s -> %s", targetSession, actualCaller)
}
