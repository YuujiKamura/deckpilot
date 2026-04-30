package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/YuujiKamura/deckpilot/pipe"
)

// IPCPipePath returns the named pipe path for the daemon.
func IPCPipePath() string {
	return `\\.\pipe\deckpilot-daemon`
}

// Daemon manages Ghostty sessions and handles IPC from the CLI.
type Daemon struct {
	mu            sync.Mutex
	sessions      map[string]string   // session name -> pipe path
	appRuntimes   map[string]string   // session name -> app runtime (winui3/win32)
	watchers      map[string]*Watcher // session name -> watcher
	lastNotify    map[string]BufferNotification
	onNotify      func(BufferNotification) // external callback (optional)
	lastUsed      map[string]string        // caller -> session name
}

// New creates a new Daemon instance.
func New() *Daemon {
	return &Daemon{
		sessions:    make(map[string]string),
		appRuntimes: make(map[string]string),
		watchers:    make(map[string]*Watcher),
		lastNotify:  make(map[string]BufferNotification),
		lastUsed:    make(map[string]string),
	}
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
		d.addSession(s.Name, s.PipePath, s.SessionFile, s.PID, s.AppRuntime)
	}
	log.Printf("daemon: discovered %d session(s)", len(sessions))

	// Periodic re-discovery of new sessions
	go func() {
		for {
			time.Sleep(30 * time.Second)
			sessions, err := pipe.Discover()
			if err != nil {
				log.Printf("re-discover warning: %v", err)
				continue
			}
			for _, s := range sessions {
				d.mu.Lock()
				_, exists := d.sessions[s.Name]
				d.mu.Unlock()
				if !exists {
					d.addSession(s.Name, s.PipePath, s.SessionFile, s.PID, s.AppRuntime)
					log.Printf("daemon: re-discovered new session %q", s.Name)
				}
			}
		}
	}()

	// Start WebSocket bridge for GitHub Pages/Web UI
	go func() {
		if err := d.ServeWS(":8099"); err != nil {
			log.Printf("daemon: WebSocket server error: %v", err)
		}
	}()

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
func (d *Daemon) addSession(name, pipePath, sessionFile string, pid int, appRuntime string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.sessions[name]; exists {
		return
	}
	d.sessions[name] = pipePath
	d.appRuntimes[name] = appRuntime

	w := NewWatcher(name, pipePath, sessionFile, pid, func(n BufferNotification) {
		d.mu.Lock()
		d.lastNotify[name] = n
		d.mu.Unlock()
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

// getWatcher returns the watcher for a session.
func (d *Daemon) getWatcher(name string) (*Watcher, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.watchers[name]
	return w, ok
}

type sessionInfo struct {
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	PipePath   string `json:"pipe_path"`
	AppRuntime string `json:"app_runtime"`
	Status     string `json:"status"`
	Uptime     string `json:"uptime"`
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
		d.addSession(s.Name, s.PipePath, s.SessionFile, s.PID, s.AppRuntime)
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
