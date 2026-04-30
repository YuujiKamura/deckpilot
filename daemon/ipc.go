package daemon

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/YuujiKamura/deckpilot/pipe"
)

// handleConn reads one line from the connection, dispatches the command,
// writes the response, and closes.
func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return
	}

	parts := strings.SplitN(line, "|", 3)
	cmd := parts[0]

	var resp string
	switch cmd {
	case "PING":
		resp = "PONG\n"
	case "SEND":
		resp = d.handleSend(parts)
	case "LIST":
		resp = d.handleList()
	case "OUTPUT":
		resp = d.handleOutput(parts)
	case "HISTORY":
		resp = d.handleHistory(parts)
	case "STATUS":
		resp = d.handleStatus(parts)
	default:
		resp = fmt.Sprintf("ERR|unknown command: %s\n", cmd)
	}

	conn.Write([]byte(resp))
}

func (d *Daemon) handleSend(parts []string) string {
	if len(parts) < 3 {
		return "ERR|usage: SEND|<name>|<base64msg>\n"
	}
	name := parts[1]
	msgB64 := parts[2]

	msgBytes, err := base64.StdEncoding.DecodeString(msgB64)
	if err != nil {
		return fmt.Sprintf("ERR|bad base64: %v\n", err)
	}
	msg := string(msgBytes)

	pipePath, ok := d.resolvePipePath(name)
	if !ok {
		d.refreshSessions()
		pipePath, ok = d.resolvePipePath(name)
		if !ok {
			return fmt.Sprintf("ERR|session not found: %s\n", name)
		}
	}

	// Pause watcher to free the pipe, send text + submit
	w, wok := d.getWatcher(name)
	if wok {
		resumeCh := w.PausePolling()
		defer close(resumeCh)
		// Small delay to ensure watcher's last poll connection is closed
		time.Sleep(100 * time.Millisecond)
	}

	// Send text via INPUT
	if err := pipe.SendKeys(pipePath, msg); err != nil {
		return fmt.Sprintf("ERR|send text: %v\n", err)
	}
	// Submit via RAW_INPUT \r (separate connection, watcher paused)
	if err := pipe.SendRaw(pipePath, []byte("\r")); err != nil {
		return fmt.Sprintf("ERR|send enter: %v\n", err)
	}

	if wok {
		time.Sleep(300 * time.Millisecond)
		return fmt.Sprintf("OK|sent (%s)\n", w.Status())
	}
	return "OK|sent\n"
}

func (d *Daemon) handleList() string {
	// Refresh before listing.
	d.refreshSessions()

	infos := d.listSessions()
	b, err := json.Marshal(infos)
	if err != nil {
		return fmt.Sprintf("ERR|json: %v\n", err)
	}
	return fmt.Sprintf("OK|%s\n", string(b))
}

func (d *Daemon) handleOutput(parts []string) string {
	if len(parts) < 3 {
		return "ERR|usage: OUTPUT|<name>|<lines>\n"
	}
	name := parts[1]

	w, ok := d.getWatcher(name)
	if !ok {
		return fmt.Sprintf("ERR|session not found: %s\n", name)
	}

	// Use watcher cache — no extra pipe hit
	content := w.LastContent()
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf("OK|%s\n", encoded)
}

func (d *Daemon) handleHistory(parts []string) string {
	if len(parts) < 2 {
		return "ERR|usage: HISTORY|<name>\n"
	}
	name := parts[1]

	w, ok := d.getWatcher(name)
	if !ok {
		return fmt.Sprintf("ERR|session not found: %s\n", name)
	}

	content, err := w.FreshHistory()
	if err != nil {
		return fmt.Sprintf("ERR|history: %v\n", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf("OK|%s\n", encoded)
}

func (d *Daemon) handleStatus(parts []string) string {
	if len(parts) < 2 {
		return "ERR|usage: STATUS|<name>\n"
	}
	name := parts[1]

	d.mu.Lock()
	n, ok := d.lastNotify[name]
	d.mu.Unlock()
	if !ok {
		// No notification yet, check watcher directly
		w, wok := d.getWatcher(name)
		if !wok {
			return fmt.Sprintf("ERR|session not found: %s\n", name)
		}
		return fmt.Sprintf("OK|%s|%s|%d\n", name, w.Status(), 0)
	}
	return fmt.Sprintf("OK|%s|%s|%d\n", n.SessionName, n.Status, n.StableFor)
}

// --- Client helpers ---

// dialDaemon connects to the daemon pipe and sends a command, returning the response.
func dialDaemon(command string) (string, error) {
	conn, err := winio.DialPipe(IPCPipePath(), nil)
	if err != nil {
		return "", fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte(command + "\n")); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return "", fmt.Errorf("no response from daemon")
	}
	return scanner.Text(), nil
}

// DaemonSend sends a message to the named session via the daemon.
// Returns status feedback from watcher (e.g. "submitted (status: idle → active)").
func DaemonSend(name, message string) (string, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(message))
	resp, err := dialDaemon(fmt.Sprintf("SEND|%s|%s", name, encoded))
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return "", fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return strings.TrimPrefix(resp, "OK|"), nil
}

// DaemonList returns the JSON array of sessions from the daemon.
func DaemonList() (string, error) {
	resp, err := dialDaemon("LIST")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return "", fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return strings.TrimPrefix(resp, "OK|"), nil
}

// DaemonOutput returns the last n lines of output for a session.
func DaemonOutput(name string, lines int) (string, error) {
	resp, err := dialDaemon(fmt.Sprintf("OUTPUT|%s|%d", name, lines))
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return "", fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	encoded := strings.TrimPrefix(resp, "OK|")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode output: %w", err)
	}
	return string(decoded), nil
}

// DaemonHistory returns the command history for a session.
func DaemonHistory(name string) (string, error) {
	resp, err := dialDaemon(fmt.Sprintf("HISTORY|%s", name))
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return "", fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	encoded := strings.TrimPrefix(resp, "OK|")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode history: %w", err)
	}
	return string(decoded), nil
}
