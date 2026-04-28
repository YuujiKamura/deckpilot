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
	"sync"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/YuujiKamura/deckpilot/pipe"
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
	protected   map[string]struct{}      // session name -> protected (advisory; issue #35)
	WSPort      int                      // WebSocket control port (default 8080)
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
		protected:   make(map[string]struct{}),
		WSPort:      8080, // Default port
	}
	if loaded := loadIdleHooks(); len(loaded) > 0 {
		d.idleHooks = loaded
		log.Printf("daemon: restored %d idle hook(s) from %s", len(loaded), idleHooksDir())
	}
	if loaded := loadProtectedSessions(); len(loaded) > 0 {
		d.protected = loaded
		log.Printf("daemon: restored %d protected session(s) from %s", len(loaded), protectedSessionsFile())
	}
	return d
}

// Run starts the daemon: auto-discovers sessions, starts watchers, and
// listens for IPC connections on the daemon pipe.
func (d *Daemon) Run() error {
	// Set up log file for daemon diagnostics
	logPath := filepath.Join(os.TempDir(), "deckpilot-daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(logFile)
		log.Printf("=== daemon started pid=%d ===", os.Getpid())
	}

	// Auto-discover existing sessions.
	sessions, err := pipe.Discover()
	if err != nil {
		log.Printf("discover warning: %v", err)
	}
	for _, s := range sessions {
		d.addSession(s.Name, s.PipePath, s.WsURL, s.SessionFile, s.PID, s.HWND, s.AppRuntime)
	}
	log.Printf("daemon: discovered %d session(s)", len(sessions))

	// Periodic re-discovery and revival of sessions (Autonomous Heartbeat)
	go func() {
		for {
			time.Sleep(5 * time.Second)
			d.refreshSessions()
		}
	}()

	// Start WebSocket bridge for GitHub Pages/Web UI
	if d.WSPort > 0 {
		go func() {
			addr := fmt.Sprintf("127.0.0.1:%d", d.WSPort)
			if err := d.ServeWS(addr); err != nil {
				log.Printf("daemon: WebSocket server error: %v", err)
			}
		}()
	} else {
		log.Printf("daemon: WebSocket server disabled (--ws-port 0)")
	}

	listener, err := winio.ListenPipe(IPCPipePath(), nil)
	if err != nil {
		return fmt.Errorf("listen pipe: %w", err)
	}
	defer listener.Close()
	log.Printf("daemon: listening on %s", IPCPipePath())

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)
			continue
		}
		go d.handleConn(conn)
	}
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
		live := false
		if w, ok := d.watchers[name]; ok {
			status = w.Status()
			p := w.Profile()
			uptime = formatUptime(time.Since(p.CreatedAt))
			pid = p.PID
			// Treat anything other than "dead" as live for the
			// protected-display filter — stale entries that no longer
			// have a real process should not advertise the ✓ flag,
			// even if the name is still in the persisted set.
			live = status != "dead"
		}
		_, isProtected := d.protected[name]
		result = append(result, sessionInfo{
			Name:       name,
			PID:        pid,
			PipePath:   pipePath,
			WsURL:      d.wsURLs[name],
			AppRuntime: d.appRuntimes[name],
			Status:     status,
			Uptime:     uptime,
			Protected:  isProtected && live,
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

// refreshSessions re-discovers sessions, attempts to revive dead watchers, and adds new ones.
func (d *Daemon) refreshSessions() {
	// Attempt to revive dead watchers instead of deleting them
	d.mu.Lock()
	var deadNames []string
	for name, w := range d.watchers {
		if w.Status() == "dead" {
			deadNames = append(deadNames, name)
			_ = w // collect names while holding lock
		}
	}
	d.mu.Unlock()

	for _, name := range deadNames {
		w, ok := d.getWatcher(name)
		if !ok {
			continue
		}
		if w.Revive() {
			// Pipe is responsive again — restart the watcher goroutine
			log.Printf("daemon: revived session %q", name)
			go w.Run(context.Background())
		} else {
			// Keep the session in maps with status "dead" so it remains visible
			log.Printf("daemon: session %q still dead, keeping entry", name)
		}
	}

	sessions, err := pipe.Discover()
	if err != nil {
		return
	}
	for _, s := range sessions {
		d.addSession(s.Name, s.PipePath, s.WsURL, s.SessionFile, s.PID, s.HWND, s.AppRuntime)
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
