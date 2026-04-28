package daemon

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
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
	case "HOOK":
		resp = d.handleHook(parts)
	case "HOOK_LIST":
		resp = d.handleHookList()
	case "SHUTDOWN":
		resp = d.handleShutdown()
	case "PROTECT":
		resp = d.handleProtect(parts)
	case "UNPROTECT":
		resp = d.handleUnprotect(parts)
	case "KILL":
		resp = d.handleKill(parts)
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

	// Phase 0: snapshot pre-typing baseline so we can detect that typing
	// landed by buffer hash (not by string-matching the sent text, which is
	// fragile to TUI display transformations like Gemini's "@path" →
	// "[Image filename]").
	tailFn := func() (string, error) { return pipe.Tail(pipePath, 40) }
	baselineBuf, _ := tailFn()
	baselineHash := BufHash(baselineBuf)

	// Phase 1: INPUT (text only, no submit yet)
	if err := pipe.SendKeys(pipePath, msg); err != nil {
		return fmt.Sprintf("ERR|send text: %v\n", err)
	}

	// Let Ghostty process INPUT before we compare buffers.
	time.Sleep(100 * time.Millisecond)

	// Phase 1.5: wait until the buffer hash diverges from the pre-typing
	// baseline — that proves the typed text landed in the TUI. Capture the
	// resulting hash as preEnterHash: the "idle with text-in-prompt" state
	// that Phase 3 will compare future samples against.
	preEnterHash := ""
	for i := 0; i < 20; i++ { // up to ~600ms
		buf, _ := tailFn()
		h := BufHash(buf)
		if h != baselineHash && buf != "" {
			preEnterHash = h
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if preEnterHash == "" {
		return "ERR|text_not_visible|phase1_timeout\n"
	}

	// Phase 2: submit via RAW_INPUT(\r) as a standalone key event.
	if err := pipe.SendEnter(pipePath); err != nil {
		return fmt.Sprintf("ERR|submit enter: %v\n", err)
	}

	// Phase 3: pure buffer-hash comparison. Any divergence from preEnterHash
	// proves Enter was accepted; 3 consecutive matches prove it was not.
	//
	// pollInterval=200ms × stableRepeatThreshold=3 = 600ms before we declare
	// stuck. This window must exceed the worst-case "Enter accepted but TUI
	// hasn't started redrawing yet" latency. Empirically Gemini CLI with a
	// @path image payload can take ~200-400ms between Enter and the first
	// visible spinner frame; 50ms × 3 = 150ms was too aggressive and caused
	// false FailedStuck on valid submits. 600ms is a safe minimum.
	result := ConfirmSubmit(tailFn, preEnterHash, 2*time.Second, 200*time.Millisecond)

	d.setLastUsed(caller, name)

	// Slash commands trigger TUI autocomplete that eats the first Enter.
	// Send a second Enter to confirm the selection.
	if strings.HasPrefix(msg, "/") {
		time.Sleep(500 * time.Millisecond)
		pipe.SendRaw(pipePath, []byte("\r"))
	}

	switch result.Status {
	case SubmitOKCleared:
		// Register callback hook if sender session is identified
		if caller != "" && d.shouldRegisterCallback(name, caller) {
			d.registerCallbackHook(name, caller)
		}
		return fmt.Sprintf("OK|submit_ok|%s|%dms\n", result.Status, result.ElapsedMs)
	case SubmitFailedStuck:
		return fmt.Sprintf("ERR|submit_failed_stuck|%dms\n", result.ElapsedMs)
	case SubmitUnconfirmed:
		return fmt.Sprintf("ERR|submit_unconfirmed|timeout_after_%dms\n", result.ElapsedMs)
	}
	return "ERR|submit_unknown\n"
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

// handleHook processes hook management commands
func (d *Daemon) handleHook(parts []string) string {
	if len(parts) < 2 {
		return "ERR|usage: HOOK|<json_payload>\n"
	}

	payload := parts[1]
	var request struct {
		Action string   `json:"action"`
		Hook   IdleHook `json:"hook"`
	}

	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return fmt.Sprintf("ERR|json decode: %v\n", err)
	}

	switch request.Action {
	case "add":
		d.AddIdleHook(request.Hook)
		return "OK\n"
	case "remove":
		d.RemoveIdleHooks()
		return "OK\n"
	default:
		return fmt.Sprintf("ERR|unknown hook action: %s\n", request.Action)
	}
}

// handleHookList returns current hooks as JSON
func (d *Daemon) handleHookList() string {
	hooks := d.ListIdleHooks()
	data, err := json.Marshal(hooks)
	if err != nil {
		return fmt.Sprintf("ERR|json encode: %v\n", err)
	}
	return string(data) + "\n"
}

// handleShutdown gracefully shuts down the daemon. Per issue #35 the
// shutdown also fans out a kill to every live session that is NOT in
// the protected set; protected sessions survive the daemon restart and
// will be re-discovered next time the daemon comes back up. The kill
// targets are snapshotted under the daemon mutex so a concurrent
// PROTECT can't race the iteration.
func (d *Daemon) handleShutdown() string {
	log.Printf("daemon: shutdown requested via IPC")
	targets := d.snapshotKillTargets()
	go func() {
		for _, name := range targets {
			if err := d.killSession(name, true); err != nil {
				// killSession only returns an error here when the
				// process has already gone away or we couldn't open a
				// handle — log and keep going so one stuck session
				// doesn't block the rest of the cascade.
				log.Printf("daemon: shutdown kill %s: %v", name, err)
			}
		}
		// Give time to send response, then exit
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}()
	return "OK|daemon shutting down\n"
}

// handleProtect adds a session to the protected set.
func (d *Daemon) handleProtect(parts []string) string {
	if len(parts) < 2 || parts[1] == "" {
		return "ERR|usage: PROTECT|<name>\n"
	}
	if err := d.Protect(parts[1]); err != nil {
		return fmt.Sprintf("ERR|%v\n", err)
	}
	return "OK\n"
}

// handleUnprotect removes a session from the protected set.
func (d *Daemon) handleUnprotect(parts []string) string {
	if len(parts) < 2 || parts[1] == "" {
		return "ERR|usage: UNPROTECT|<name>\n"
	}
	if err := d.Unprotect(parts[1]); err != nil {
		return fmt.Sprintf("ERR|%v\n", err)
	}
	return "OK\n"
}

// handleKill terminates the named session. The third argument (force)
// must be the literal string "true" or "false" — any other value is a
// usage error. Protected sessions return ERR|protected: use --force
// unless force is true.
func (d *Daemon) handleKill(parts []string) string {
	if len(parts) < 3 || parts[1] == "" {
		return "ERR|usage: KILL|<name>|<force>\n"
	}
	force := false
	switch parts[2] {
	case "true":
		force = true
	case "false":
		force = false
	default:
		return "ERR|usage: KILL|<name>|<force>\n"
	}
	if err := d.killSession(parts[1], force); err != nil {
		return fmt.Sprintf("ERR|%v\n", err)
	}
	return "OK\n"
}

// DaemonAddIdleHook adds an idle hook via daemon IPC
func DaemonAddIdleHook(hook IdleHook) error {
	request := map[string]interface{}{
		"action": "add",
		"hook":   hook,
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}

	resp, err := dialDaemon(fmt.Sprintf("HOOK|%s", string(data)))
	if err != nil {
		return fmt.Errorf("dial daemon: %w", err)
	}
	if strings.HasPrefix(resp, "ERR|") {
		return fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return nil
}

// DaemonRemoveIdleHooks removes all idle hooks via daemon IPC
func DaemonRemoveIdleHooks() error {
	request := map[string]interface{}{
		"action": "remove",
		"hook":   IdleHook{},
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}

	resp, err := dialDaemon(fmt.Sprintf("HOOK|%s", string(data)))
	if err != nil {
		return fmt.Errorf("dial daemon: %w", err)
	}
	if strings.HasPrefix(resp, "ERR|") {
		return fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return nil
}

// DaemonListIdleHooks retrieves current idle hooks via daemon IPC
func DaemonListIdleHooks() ([]IdleHook, error) {
	resp, err := dialDaemon("HOOK_LIST")
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}

	var hooks []IdleHook
	if err := json.Unmarshal([]byte(resp), &hooks); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return hooks, nil
}

// DaemonShutdown sends shutdown command to daemon
func DaemonShutdown() (string, error) {
	resp, err := dialDaemon("SHUTDOWN")
	if err != nil {
		return "", fmt.Errorf("dial daemon: %w", err)
	}
	return resp, nil
}

// DaemonProtect adds the named session to the daemon's protected set
// (issue #35). Returns nil on success, or an error string from the
// daemon (the ERR| prefix is stripped). Calling on an already-protected
// session is a no-op success.
func DaemonProtect(name string) error {
	resp, err := dialDaemon(fmt.Sprintf("PROTECT|%s", name))
	if err != nil {
		return fmt.Errorf("dial daemon: %w", err)
	}
	if strings.HasPrefix(resp, "ERR|") {
		return fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return nil
}

// DaemonUnprotect removes the named session from the protected set.
// Idempotent: unprotecting a non-protected session is a no-op success.
func DaemonUnprotect(name string) error {
	resp, err := dialDaemon(fmt.Sprintf("UNPROTECT|%s", name))
	if err != nil {
		return fmt.Errorf("dial daemon: %w", err)
	}
	if strings.HasPrefix(resp, "ERR|") {
		return fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return nil
}

// DaemonKill terminates the named session via the daemon. If force is
// false and the session is protected, the daemon returns
// "protected: use --force" which surfaces here as an error so the CLI
// can print it and exit non-zero.
func DaemonKill(name string, force bool) error {
	forceStr := "false"
	if force {
		forceStr = "true"
	}
	resp, err := dialDaemon(fmt.Sprintf("KILL|%s|%s", name, forceStr))
	if err != nil {
		return fmt.Errorf("dial daemon: %w", err)
	}
	if strings.HasPrefix(resp, "ERR|") {
		return fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return nil
}
