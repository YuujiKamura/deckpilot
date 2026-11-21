package daemon

import (
	"bufio"
	"io"
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
	scanner.Buffer(make([]byte, 64*1024), maxIPCLine) // a long SEND message is one line too
	if !scanner.Scan() {
		return
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return
	}

	parts := strings.SplitN(line, "|", 5)
	cmd := parts[0]
	// Trace-only: PING fires every 1-2s per CLI tab; logging at INFO
	// turns the daemon log into a multi-MB/hour ring buffer. Gate
	// behind DECKPILOT_DEBUG; lifecycle/error lines stay on log.Printf.
	debugf("handleConn: raw line=%q parts=%v", line, parts)

	var resp string
	switch cmd {
	case "PING":
		resp = "PONG\n"
	case "SEND":
		resp = d.handleSend(parts)
	case "SENDRAW":
		resp = d.handleSendRaw(parts)
	case "PRUNE":
		resp = d.handlePrune(parts)
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
	default:
		resp = fmt.Sprintf("ERR|unknown command: %s\n", cmd)
	}

	conn.Write([]byte(resp))
}

func (d *Daemon) handlePrune(parts []string) string {
	if len(parts) != 2 || parts[1] == "" || strings.ContainsAny(parts[1], "|\r\n") {
		return "ERR|usage: PRUNE|<session>\n"
	}
	name := parts[1]
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.watchers[name]
	if !ok || w.Status() != "dead" {
		return fmt.Sprintf("ERR|session %q is not tracked as dead\n", name)
	}
	delete(d.sessions, name)
	delete(d.wsURLs, name)
	delete(d.appRuntimes, name)
	delete(d.watchers, name)
	delete(d.lastNotify, name)
	log.Printf("daemon: manually pruned dead session %q", name)
	return fmt.Sprintf("OK|pruned|%s\n", name)
}

func (d *Daemon) handleSendRaw(parts []string) string {
	if len(parts) != 3 || strings.ContainsAny(parts[1], "|\r\n") {
		return "ERR|usage: SENDRAW|<name>|<base64bytes>\n"
	}
	name := parts[1]
	data, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Sprintf("ERR|bad base64: %v\n", err)
	}
	if len(data) == 0 {
		return "ERR|empty raw input\n"
	}
	pipePath, ok := d.resolvePipePath(name)
	if !ok || pipePath == "" {
		return fmt.Sprintf("ERR|no pipe for session %q\n", name)
	}
	if _, err := pipe.SendRaw(pipePath, data); err != nil {
		return fmt.Sprintf("ERR|send raw: %v\n", err)
	}
	return "OK|sent raw\n"
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
			return sessionNotFoundResp(name)
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
	w, ok := d.getWatcher(name)
	initialStatus := "unknown"
	if ok {
		initialStatus = w.Status()
		// Revive dead watcher before pausing
		if w.Status() == "dead" {
			if w.Revive() {
				initialStatus = w.Status()
			}
		}
		if w.Status() != "dead" {
			// 2s window: if the runner is alive it picks up almost
			// instantly. If it does not, the goroutine is gone or wedged
			// — proceed without pausing rather than blocking handleSend.
			rc, err := w.PausePolling(2 * time.Second)
			if err != nil {
				log.Printf("watcher[%s]: PausePolling failed (%v); proceeding unpaused", name, err)
			} else {
				resumeCh = rc
				defer close(resumeCh)
			}
		} else {
			w = nil // don't use dead watcher for Client() access
		}
	}

	// Empty message = just send Enter directly
	if msg == "" {
		var err error
		if w != nil {
			_, err = w.Client().SendRaw([]byte("\r"))
		} else {
			err = pipe.SendEnter(pipePath)
		}
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
	tailFn := func() (string, error) {
		if w != nil {
			return w.Client().Tail(40)
		}
		return pipe.Tail(pipePath, 40)
	}
	baselineBuf, _ := tailFn()
	baselineHash := BufHash(baselineBuf)

	// Phase 1: INPUT (text only, no submit yet)
	var errPhase1 error
	if w != nil {
		_, errPhase1 = w.Client().SendKeys(msg)
	} else {
		_, errPhase1 = pipe.SendKeys(pipePath, msg)
	}
	if errPhase1 != nil {
		return fmt.Sprintf("ERR|send text: %v\n", errPhase1)
	}

	// Let Ghostty process INPUT before we compare buffers.
	time.Sleep(100 * time.Millisecond)

	// Phase 1.5: wait until the buffer hash diverges from the pre-typing
	// baseline — that proves the typed text landed in the TUI. Capture the
	// resulting hash as preEnterHash: the "idle with text-in-prompt" state
	// that Phase 3 will compare future samples against.
	// 2026-06-15 fix: claude (Anthropic TUI) は alt screen で input box を持つので、
	// SendKeys で input box に書き込まれた typed text は ghostty scrollback に echo されない。
	// 旧版は「scrollback hash 変化 = typed visible」 と短絡判定し、 alt screen TUI を
	// 「text_not_visible」 と誤判定して ghostty を kill していた (user 観察「ClaudeCode は普通に
	// 起動してるのに kill されてる」 で確定)。 phase1.5 を 「hash 変化があれば preEnterHash を更新、
	// 変化が見えなくても baselineHash を preEnterHash として fall-through」 に変更。 typed の
	// 失敗検出は Phase 3 (ConfirmSubmit) の stuck 判定に委ねる ── Enter で claude が応答すれば
	// alt screen でも spinner / 応答 line で必ず描画変化する。
	preEnterHash := baselineHash
	for i := 0; i < 20; i++ { // up to ~600ms — early-break on visible diverge
		buf, _ := tailFn()
		h := BufHash(buf)
		if h != baselineHash && buf != "" {
			preEnterHash = h
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Phase 2: submit via RAW_INPUT(\r) as a standalone key event.
	var errPhase2 error
	if w != nil {
		_, errPhase2 = w.Client().SendRaw([]byte("\r"))
	} else {
		errPhase2 = pipe.SendEnter(pipePath)
	}
	if errPhase2 != nil {
		return fmt.Sprintf("ERR|submit enter: %v\n", errPhase2)
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
		if w != nil {
			_, _ = w.Client().SendRaw([]byte("\r"))
		} else {
			_, _ = pipe.SendRaw(pipePath, []byte("\r"))
		}
	}

	switch result.Status {
	case SubmitOKCleared:
		// Register callback hook if sender session is identified
		if caller != "" && d.shouldRegisterCallback(name, caller) {
			d.registerCallbackHook(name, caller)
		}
		outcome := classifySendOutcome(initialStatus, result.Status)
		return fmt.Sprintf("OK|submit_ok|%s|%s|%dms\n", outcome, result.Status, result.ElapsedMs)
	case SubmitFailedStuck:
		return fmt.Sprintf("ERR|no-effect|submit_failed_stuck|%dms\n", result.ElapsedMs)
	case SubmitUnconfirmed:
		return fmt.Sprintf("ERR|submit_unconfirmed|timeout_after_%dms\n", result.ElapsedMs)
	}
	return "ERR|submit_unknown\n"
}

func classifySendOutcome(initialStatus string, submit SubmitStatus) string {
	if submit == SubmitFailedStuck {
		return "no-effect"
	}
	if initialStatus == "active" {
		return "queued"
	}
	return "accepted"
}

func (d *Daemon) handleList() string {
	// Refresh before listing.
	d.refreshSessions()

	infos := d.listSessions()
	addTabs(infos)
	b, err := json.Marshal(infos)
	if err != nil {
		return fmt.Sprintf("ERR|json: %v\n", err)
	}
	return fmt.Sprintf("OK|%s\n", string(b))
}

// composeShowStatus builds the status field returned in the SHOW response.
// When FreshTail (or any fresh-content path) failed and the caller is about
// to return cached LastContent, the status is prefixed with "stale:" so the
// client can distinguish a live buffer from a frozen cache.
//
// Defensive normalisation:
//   - Empty status → "unknown" (avoids bare "stale:" or empty-field parsing).
//   - "|", CR, LF stripped from the status (wire format is
//     "OK|<base64>|<status>\n" — these characters in the status would
//     break the client parser or split the response into multiple lines,
//     which is a protocol-injection surface).
//   - "stale:" not double-prefixed (defensive against cascaded fallbacks).
func composeShowStatus(freshErr error, watcherStatus string) string {
	s := watcherStatus
	if s == "" {
		s = "unknown"
	}
	s = sanitizeStatusToken(s)
	if freshErr != nil && !strings.HasPrefix(s, "stale:") {
		s = "stale:" + s
	}
	return s
}

// statusTokenSanitizer is reused across all SHOW responses; allocating a
// fresh Replacer per call would be wasteful on the IPC hot path.
var statusTokenSanitizer = strings.NewReplacer("|", "_", "\r", "_", "\n", "_")

// sanitizeStatusToken replaces any wire-format separator with "_" so the
// status field cannot inject extra response fields or split the line.
func sanitizeStatusToken(s string) string {
	return statusTokenSanitizer.Replace(s)
}

func (d *Daemon) handleShow(parts []string) string {
	debugf("handleShow: parts=%v len=%d", parts, len(parts))
	// parts: ["SHOW", name, mode, caller]
	caller := ""
	if len(parts) >= 4 {
		caller = parts[3]
	}

	name := ""
	if len(parts) >= 2 {
		name = parts[1]
	}

	debugf("handleShow: caller=%q name=%q", caller, name)

	// Resolve name from lastUsed if empty
	if name == "" {
		if caller == "" {
			debugf("handleShow: caller is empty, returning error")
			return "ERR|session name required\n"
		}
		debugf("handleShow: looking up lastUsed for caller=%q", caller)
		last, ok := d.getLastUsed(caller)
		debugf("handleShow: getLastUsed(%q) = %q, ok=%v", caller, last, ok)
		if !ok {
			return "ERR|no recent session for this caller\n"
		}
		name = last
	}

	mode := "buffer"
	if len(parts) >= 3 && parts[2] != "" {
		mode = parts[2]
	}

	// A tab target bypasses the watcher: the watcher caches only the active
	// tab, so a worker running in a background tab must be read straight
	// off the pipe with the tab named in the request.
	tab := ""
	if len(parts) >= 5 {
		tab = strings.TrimSpace(parts[4])
	}
	if tab != "" || mode == "tabs" {
		return d.showTab(name, caller, mode, tab)
	}

	w, ok := d.getWatcher(name)
	if !ok {
		d.refreshSessions()
		w, ok = d.getWatcher(name)
		if !ok {
			return sessionNotFoundResp(name)
		}
	}

	// If watcher is dead, try to revive it before giving up
	if w.Status() == "dead" {
		if !w.Revive() {
			// Revive failed — try full session rediscovery
			d.refreshSessions()
			w, ok = d.getWatcher(name)
			if !ok {
				return sessionNotFoundResp(name)
			}
		}
	}

	var content string
	var err error
	// freshErr captures whether the buffer in `content` is a live read.
	// nil = fresh; non-nil = falling back to LastContent or stale source.
	// Cleared when a direct-pipe fallback succeeds.
	var freshErr error
	switch mode {
	case "history":
		content, err = w.FreshHistory()
		if err != nil {
			freshErr = err
			// Fallback to direct pipe access — a direct success is live, so
			// clear staleness; otherwise we error out and never reach the
			// composeShowStatus path.
			if pipePath, pipeOk := d.resolvePipePath(name); pipeOk {
				if direct, directErr := pipe.History(pipePath); directErr == nil && direct != "" {
					content = direct
					log.Printf("handleShow: recovered history via direct pipe.History for %s", name)
					err = nil
					freshErr = nil
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
			freshErr = err
			content = w.LastContent()
		}
		// If content is still empty, try direct pipe access as fallback.
		// A direct success clears staleness — that read is live.
		if content == "" {
			if pipePath, pipeOk := d.resolvePipePath(name); pipeOk {
				if direct, directErr := pipe.Tail(pipePath, 50); directErr == nil && direct != "" {
					content = direct
					freshErr = nil
					log.Printf("handleShow: recovered content via direct pipe.Tail for %s", name)
				}
			}
		}
	}

	d.setLastUsed(caller, name)

	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf("OK|%s|%s\n", encoded, composeShowStatus(freshErr, w.Status()))
}

// showTab answers a SHOW that names a tab (or asks for the tab list) by
// reading the session pipe directly. The status field is "live tab=<t>"
// because the watcher status describes the active tab, not this one.
func (d *Daemon) showTab(name, caller, mode, tab string) string {
	if tab != "" && !pipe.ValidTab(tab) {
		return fmt.Sprintf("ERR|invalid tab %q (want an index like 0 or id=t_001)\n", tab)
	}
	pipePath, ok := d.resolvePipePath(name)
	if !ok {
		d.refreshSessions()
		if pipePath, ok = d.resolvePipePath(name); !ok {
			return sessionNotFoundResp(name)
		}
	}

	if mode != "tabs" {
		// The terminal answers an unknown tab with the active tab's text, which
		// would show the wrong worker under the requested tab's name.
		list, err := pipe.ListTabs(pipePath)
		if err != nil {
			return fmt.Sprintf("ERR|tabs: %s\n", sanitizeErr(err))
		}
		if !pipe.TabInList(list, tab) {
			return fmt.Sprintf("ERR|no tab %q in %s (see: deckpilot show %s --tabs)\n", tab, name, name)
		}
	}

	var content, status string
	var err error
	switch mode {
	case "tabs":
		content, err = pipe.ListTabs(pipePath)
		status = "live tabs"
	case "history":
		content, err = pipe.HistoryTab(pipePath, tab)
		status = "live tab=" + tab
	default:
		content, err = pipe.TailTab(pipePath, 50, tab)
		status = "live tab=" + tab
	}
	if err != nil {
		return fmt.Sprintf("ERR|%s: %s\n", mode, sanitizeErr(err))
	}

	d.setLastUsed(caller, name)
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf("OK|%s|%s\n", encoded, status)
}

// sanitizeErr flattens an error onto one line so it cannot break the
// line-oriented IPC reply.
func sanitizeErr(err error) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
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
	debugf("sendViaWS: sending %q to %s", payload, wsURL)

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
	debugf("sendViaWS: response %q", resp)
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

	return readReplyLine(conn)
}

// maxIPCLine bounds one request or reply line. SHOW --history returns the whole
// scrollback base64-encoded on one line, which is far past bufio's 64 KiB default
// (that limit made `show --history` fail as "no response from daemon").
const maxIPCLine = 64 << 20

// readReplyLine reads one reply line. An oversized or broken reply reports the
// real reason instead of looking like a silent daemon.
func readReplyLine(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxIPCLine)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read daemon reply: %w", err)
		}
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
		return "", errFromResponse(strings.TrimPrefix(resp, "ERR|"))
	}
	return strings.TrimPrefix(resp, "OK|"), nil
}

// DaemonSendRaw sends key bytes without appending Enter.
func DaemonSendRaw(name string, data []byte) error {
	if strings.ContainsAny(name, "|\r\n") {
		return fmt.Errorf("invalid session name for raw send")
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	resp, err := dialDaemon(fmt.Sprintf("SENDRAW|%s|%s", name, encoded))
	if err != nil {
		return err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	if !strings.HasPrefix(resp, "OK|") {
		return fmt.Errorf("unexpected raw-send response: %q", resp)
	}
	return nil
}

func DaemonPrune(name string) error {
	if strings.ContainsAny(name, "|\r\n") {
		return fmt.Errorf("invalid session name for prune")
	}
	resp, err := dialDaemon(fmt.Sprintf("PRUNE|%s", name))
	if err != nil {
		return err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	if !strings.HasPrefix(resp, "OK|pruned|") {
		return fmt.Errorf("unexpected prune response: %q", resp)
	}
	return nil
}

// DaemonList returns the JSON array of sessions from the daemon.
func DaemonList() (string, error) {
	resp, err := dialDaemon("LIST")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return "", errFromResponse(strings.TrimPrefix(resp, "ERR|"))
	}
	return strings.TrimPrefix(resp, "OK|"), nil
}

// DaemonShow retrieves session content (buffer or history) with caller tracking.
func DaemonShow(name, mode, caller string) (content string, status string, err error) {
	return DaemonShowTab(name, mode, caller, "")
}

// DaemonShowTab is DaemonShow aimed at one tab of the session ("0",
// "id=t_001"). An empty tab reads the active tab through the watcher, as
// DaemonShow does. mode "tabs" returns the raw LIST_TABS reply.
func DaemonShowTab(name, mode, caller, tab string) (content string, status string, err error) {
	req := fmt.Sprintf("SHOW|%s|%s|%s", name, mode, caller)
	if tab != "" {
		req += "|" + tab
	}
	resp, err := dialDaemon(req)
	if err != nil {
		return "", "", err
	}
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "ERR|") {
		return "", "", errFromResponse(strings.TrimPrefix(resp, "ERR|"))
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
	status = strings.TrimSpace(parts[1])
	if err := checkTabReply(mode, tab, status); err != nil {
		return "", "", err
	}
	return string(decoded), status, nil
}

// checkTabReply refuses a reply to a per-tab SHOW that did not come from
// showTab. A daemon built before tab support drops the extra field and
// answers with the active tab, which would pass off one worker's screen as
// another's.
func checkTabReply(mode, tab, status string) error {
	stale := fmt.Errorf("the running daemon does not support tabs (status %q); run 'deckpilot shutdown' and retry", status)
	if mode == "tabs" {
		if status != "live tabs" {
			return stale
		}
		return nil
	}
	if tab != "" && status != "live tab="+tab {
		return stale
	}
	return nil
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

// handleShutdown gracefully shuts down the daemon
func (d *Daemon) handleShutdown() string {
	log.Printf("daemon: shutdown requested via IPC")
	go func() {
		// Give time to send response, then exit
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}()
	return "OK|daemon shutting down\n"
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
