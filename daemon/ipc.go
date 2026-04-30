package daemon

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/YuujiKamura/deckpilot/pipe"
	"github.com/gorilla/websocket"
)

// Version variables for the daemon process.
// These are set via -ldflags at build time (same binary as main).
var Version = "dev"
var Commit = "unknown"
var BuildTime = "unknown"

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

	parts := strings.SplitN(line, "|", 5)
	cmd := parts[0]
	log.Printf("handleConn: raw line=%q parts=%v", line, parts)

	var resp string
	switch cmd {
	case "PING":
		resp = "PONG\n"
	case "SEND":
		resp = d.handleSend(parts)
	case "LIST":
		resp = d.handleList()
	case "SHOW":
		resp = d.handleShow(parts)
	case "VERSION":
		resp = handleVersion()
	default:
		resp = fmt.Sprintf("ERR|unknown command: %s\n", cmd)
	}

	conn.Write([]byte(resp))
}

func handleVersion() string {
	type versionInfo struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		BuiltAt string `json:"built_at"`
	}
	info := versionInfo{
		Version: Version,
		Commit:  Commit,
		BuiltAt: BuildTime,
	}
	b, err := json.Marshal(info)
	if err != nil {
		return fmt.Sprintf("ERR|json: %v\n", err)
	}
	return fmt.Sprintf("OK|%s\n", string(b))
}

func (d *Daemon) handleSend(parts []string) string {
	if len(parts) < 3 {
		return "ERR|usage: SEND|<name>|<base64msg>\n"
	}
	name := parts[1]
	msgB64 := parts[2]

	caller := ""
	if len(parts) >= 4 {
		caller = parts[3]
	}

	msgBytes, err := base64.StdEncoding.DecodeString(msgB64)
	if err != nil {
		return fmt.Sprintf("ERR|bad base64: %v\n", err)
	}
	msg := string(msgBytes)

	pipePath, ok := d.resolvePipePath(name)
	if !ok {
		d.refreshSessions()
		pipePath, ok = d.resolvePipePath(name)
	}

	// If no pipe path, try WebSocket (web sessions)
	if !ok || pipePath == "" {
		wsURL, wsOk := d.resolveWsURL(name)
		if !wsOk {
			d.refreshSessions()
			wsURL, wsOk = d.resolveWsURL(name)
		}
		if !wsOk || wsURL == "" {
			return fmt.Sprintf("ERR|session not found: %s\n", name)
		}
		// Send via WebSocket using CP protocol
		payload := msg + "\r"
		resp, err := sendViaWS(wsURL, payload)
		if err != nil {
			return fmt.Sprintf("ERR|ws send: %v\n", err)
		}
		d.setLastUsed(caller, name)
		return fmt.Sprintf("OK|ws|%s\n", resp)
	}

	var resumeCh chan struct{}
	if w, ok := d.getWatcher(name); ok {
		// Revive dead watcher before pausing
		if w.Status() == "dead" {
			w.Revive()
		}
		if w.Status() != "dead" {
			resumeCh = w.PausePolling()
			defer close(resumeCh)
		}
	}

	// Empty message = just send Enter directly
	if msg == "" {
		err := pipe.SendEnter(pipePath)
		if err != nil {
			return fmt.Sprintf("ERR|send: %v\n", err)
		}
		d.setLastUsed(caller, name)
		return "OK|sent|enter\n"
	}

	// Capture buffer hash before send for retry detection
	preHash := ""
	if pre, err := pipe.Tail(pipePath, 50); err == nil {
		preHash = fmt.Sprintf("%x", sha256.Sum256([]byte(pre)))
	}

	// Phase 1: INPUT(text) → ACK wait → Phase 2: RAW_INPUT(\r) → ACK wait
	submitID, err := pipe.SendWithSubmit(pipePath, msg, "\r")
	if err != nil {
		return fmt.Sprintf("ERR|send: %v\n", err)
	}

	// Retry \r if buffer didn't change (known Ghostty CP drain issue)
	if preHash != "" {
		for retry := 0; retry < 5; retry++ {
			time.Sleep(500 * time.Millisecond)
			post, err := pipe.Tail(pipePath, 50)
			if err != nil {
				break
			}
			postHash := fmt.Sprintf("%x", sha256.Sum256([]byte(post)))
			if postHash != preHash {
				log.Printf("handleSend: buffer changed after %d retries", retry)
				break
			}
			log.Printf("handleSend: buffer unchanged, resending \\r (retry %d)", retry+1)
			pipe.SendRaw(pipePath, []byte("\r"))
		}
	}

	// Slash commands trigger TUI autocomplete that eats the first Enter.
	// Send a second Enter to confirm the selection.
	if strings.HasPrefix(msg, "/") {
		time.Sleep(500 * time.Millisecond)
		pipe.SendRaw(pipePath, []byte("\r"))
	}

	d.setLastUsed(caller, name)

	if submitID == 0 {
		return "OK|sent|no_ack\n"
	}
	return fmt.Sprintf("OK|ack|%d\n", submitID)
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

func (d *Daemon) handleShow(parts []string) string {
	log.Printf("handleShow: parts=%v len=%d", parts, len(parts))
	// parts: ["SHOW", name, mode, caller]
	caller := ""
	if len(parts) >= 4 {
		caller = parts[3]
	}

	name := ""
	if len(parts) >= 2 {
		name = parts[1]
	}

	log.Printf("handleShow: caller=%q name=%q", caller, name)

	// Resolve name from lastUsed if empty
	if name == "" {
		if caller == "" {
			log.Printf("handleShow: caller is empty, returning error")
			return "ERR|session name required\n"
		}
		log.Printf("handleShow: looking up lastUsed for caller=%q", caller)
		last, ok := d.getLastUsed(caller)
		log.Printf("handleShow: getLastUsed(%q) = %q, ok=%v", caller, last, ok)
		if !ok {
			return "ERR|no recent session for this caller\n"
		}
		name = last
	}

	mode := "buffer"
	if len(parts) >= 3 && parts[2] != "" {
		mode = parts[2]
	}

	w, ok := d.getWatcher(name)
	if !ok {
		d.refreshSessions()
		w, ok = d.getWatcher(name)
		if !ok {
			return fmt.Sprintf("ERR|session not found: %s\n", name)
		}
	}

	// If watcher is dead, try to revive it before giving up
	if w.Status() == "dead" {
		if !w.Revive() {
			// Revive failed — try full session rediscovery
			d.refreshSessions()
			w, ok = d.getWatcher(name)
			if !ok {
				return fmt.Sprintf("ERR|session not found: %s\n", name)
			}
		}
	}

	var content string
	var err error
	switch mode {
	case "history":
		content, err = w.FreshHistory()
		if err != nil {
			// Fallback to direct pipe access
			if pipePath, pipeOk := d.resolvePipePath(name); pipeOk {
				if direct, directErr := pipe.History(pipePath); directErr == nil && direct != "" {
					content = direct
					log.Printf("handleShow: recovered history via direct pipe.History for %s", name)
					err = nil
				}
			}
			if err != nil {
				return fmt.Sprintf("ERR|history: %v\n", err)
			}
		}
	default:
		content, err = w.FreshTail(50)
		if err != nil {
			log.Printf("handleShow: FreshTail error: %v, falling back to LastContent", err)
			content = w.LastContent()
		}
		// If content is still empty, try direct pipe access as fallback
		if content == "" {
			if pipePath, pipeOk := d.resolvePipePath(name); pipeOk {
				if direct, directErr := pipe.Tail(pipePath, 50); directErr == nil && direct != "" {
					content = direct
					log.Printf("handleShow: recovered content via direct pipe.Tail for %s", name)
				}
			}
		}
	}

	d.setLastUsed(caller, name)

	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf("OK|%s|%s\n", encoded, w.Status())
}

// sendViaWS sends a message to a ghostty-web session via WebSocket using the CP protocol.
// Protocol: INPUT|{from}|{base64text} → QUEUED|ghostty-web|INPUT|{cmdId}
func sendViaWS(wsURL, msg string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "", fmt.Errorf("parse ws url: %w", err)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	encoded := base64.StdEncoding.EncodeToString([]byte(msg))
	payload := fmt.Sprintf("INPUT|deckpilot|%s", encoded)
	log.Printf("sendViaWS: sending %q to %s", payload, wsURL)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		return "", fmt.Errorf("ws write: %w", err)
	}

	// Read response with timeout
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, respBytes, err := conn.ReadMessage()
	if err != nil {
		return "", fmt.Errorf("ws read: %w", err)
	}
	resp := string(respBytes)
	log.Printf("sendViaWS: response %q", resp)
	return resp, nil
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
func DaemonSend(name, message, caller string) (string, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(message))
	resp, err := dialDaemon(fmt.Sprintf("SEND|%s|%s|%s", name, encoded, caller))
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

// DaemonShow retrieves session content (buffer or history) with caller tracking.
func DaemonShow(name, mode, caller string) (content string, status string, err error) {
	resp, err := dialDaemon(fmt.Sprintf("SHOW|%s|%s|%s", name, mode, caller))
	if err != nil {
		return "", "", err
	}
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "ERR|") {
		return "", "", fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	// Response: OK|<base64content>|<status>
	body := strings.TrimPrefix(resp, "OK|")
	parts := strings.SplitN(body, "|", 2)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("unexpected response: %s", resp)
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("decode: %w", err)
	}
	return string(decoded), strings.TrimSpace(parts[1]), nil
}
