package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
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
	mu       sync.Mutex
	sessions map[string]string   // session name -> pipe path
	watchers map[string]*Watcher // session name -> watcher
}

// New creates a new Daemon instance.
func New() *Daemon {
	return &Daemon{
		sessions: make(map[string]string),
		watchers: make(map[string]*Watcher),
	}
}

// Run starts the daemon: auto-discovers sessions, starts watchers, and
// listens for IPC connections on the daemon pipe.
func (d *Daemon) Run() error {
	// Auto-discover existing sessions.
	sessions, err := pipe.Discover()
	if err != nil {
		log.Printf("discover warning: %v", err)
	}
	for _, s := range sessions {
		d.addSession(s.Name, s.PipePath)
	}
	log.Printf("daemon: discovered %d sessions", len(sessions))

	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:(A;;GA;;;WD)",
	}
	listener, err := winio.ListenPipe(IPCPipePath(), cfg)
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
func (d *Daemon) addSession(name, pipePath string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.sessions[name]; exists {
		return
	}
	d.sessions[name] = pipePath

	w := NewWatcher(name, pipePath)
	d.watchers[name] = w
	go w.Run(nil)
	log.Printf("daemon: added session %q -> %s", name, pipePath)
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
	Name     string `json:"name"`
	PipePath string `json:"pipe_path"`
	Status   string `json:"status"`
}

// listSessions returns a snapshot of all session info.
func (d *Daemon) listSessions() []sessionInfo {
	d.mu.Lock()
	defer d.mu.Unlock()

	var result []sessionInfo
	for name, pipePath := range d.sessions {
		status := "unknown"
		if w, ok := d.watchers[name]; ok {
			status = w.Status()
		}
		result = append(result, sessionInfo{
			Name:     name,
			PipePath: pipePath,
			Status:   status,
		})
	}
	return result
}

// refreshSessions re-discovers sessions and adds any new ones.
func (d *Daemon) refreshSessions() {
	sessions, err := pipe.Discover()
	if err != nil {
		return
	}
	for _, s := range sessions {
		d.addSession(s.Name, s.PipePath)
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
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
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
