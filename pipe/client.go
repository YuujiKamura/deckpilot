package pipe

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	winio "github.com/Microsoft/go-winio"
)

const dialTimeout = 2 * time.Second
const readDeadline = 5 * time.Second

// Client represents a persistent named pipe client.
type Client struct {
	mu       sync.Mutex
	pipePath string
	conn     net.Conn
}

// NewClient creates a new persistent client for the given pipe path.
func NewClient(pipePath string) *Client {
	return &Client{
		pipePath: pipePath,
	}
}

// Close closes the underlying connection if it exists.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// SendRecv performs a named pipe exchange, reusing the connection if possible.
func (c *Client) SendRecv(message string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}

	// Try up to 2 times (initial + one reconnect)
	for i := 0; i < 2; i++ {
		if c.conn == nil {
			ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			conn, err := winio.DialPipeContext(ctx, c.pipePath)
			cancel()
			if err != nil {
				return "", fmt.Errorf("dial %s: %w", c.pipePath, err)
			}
			c.conn = conn
		}

		c.conn.SetDeadline(time.Now().Add(readDeadline))
		if _, err := c.conn.Write([]byte(message)); err != nil {
			// Write failed, close and retry once
			c.conn.Close()
			c.conn = nil
			continue
		}

		// Read first response.
		tmp := make([]byte, 65536)
		n, err := c.conn.Read(tmp)
		if err != nil {
			// Read failed, close and retry once
			c.conn.Close()
			c.conn = nil
			continue
		}
		resp := strings.TrimRight(string(tmp[:n]), "\r\n")

		// TAIL and HISTORY replies are a header line plus the screen text, which can
		// be far longer than one read (a long scrollback is hundreds of KiB).
		if hasBodyAfterHeader(resp) {
			// Read remaining content until EOF or timeout
			var buf bytes.Buffer
			buf.WriteString(string(tmp[:n]))
			for {
				nn, err := c.conn.Read(tmp)
				if nn > 0 {
					buf.Write(tmp[:nn])
				}
				if err != nil {
					// We expect EOF eventually. For named pipes, Read might return
					// an error when the other side isn't writing more, but we
					// should check if it's a timeout.
					break
				}
			}
			return strings.TrimRight(buf.String(), "\r\n"), nil
		}

		return resp, nil
	}

	return "", fmt.Errorf("persistent sendrecv failed after retry")
}

// hasBodyAfterHeader reports whether a reply is "<header>\n<text>" whose text
// must be read to the end (control plane formatTail / formatHistory, and the
// one-line-per-tab LIST_TABS reply).
func hasBodyAfterHeader(resp string) bool {
	return strings.HasPrefix(resp, "TAIL|") || strings.HasPrefix(resp, "HISTORY|") ||
		strings.HasPrefix(resp, "LIST_TABS|")
}

// SendRecv performs a one-shot named pipe exchange: dial, write message,
// read response until EOF, close. (Legacy wrapper)
func SendRecv(pipePath, message string) (string, error) {
	client := NewClient(pipePath)
	defer client.Close()
	return client.SendRecv(message)
}

// SendKeys sends base64-encoded keystrokes via the INPUT command.
// Returns the cmd_id and error.
func (c *Client) SendKeys(text string) (uint32, error) {
	msg := fmt.Sprintf("INPUT|deckpilot|%s", Base64Encode(text))
	resp, err := c.SendRecv(msg)
	if err != nil {
		return 0, fmt.Errorf("sendkeys: %w", err)
	}
	if errMsg, ok := IsError(resp); ok {
		return 0, fmt.Errorf("server: %s", errMsg)
	}
	id, _ := ParseCmdID(resp)
	return id, nil
}

// SendKeys is a legacy wrapper for Client.SendKeys.
func SendKeys(pipePath, text string) (uint32, error) {
	client := NewClient(pipePath)
	defer client.Close()
	return client.SendKeys(text)
}

// SendRaw sends raw bytes via RAW_INPUT (not interpreted as text).
// Falls back to INPUT if RAW_INPUT is not supported.
// Returns the cmd_id and error.
func (c *Client) SendRaw(data []byte) (uint32, error) {
	encoded := base64.StdEncoding.EncodeToString(data)
	msg := fmt.Sprintf("RAW_INPUT|deckpilot|%s", encoded)
	resp, err := c.SendRecv(msg)
	if err != nil {
		return 0, fmt.Errorf("sendraw: %w", err)
	}
	if errMsg, ok := IsError(resp); ok {
		// Fallback to INPUT if server doesn't support RAW_INPUT
		if strings.Contains(errMsg, "PARSE_ERROR") || strings.Contains(errMsg, "unknown") {
			return c.SendKeys(string(data))
		}
		return 0, fmt.Errorf("server: %s", errMsg)
	}
	id, _ := ParseCmdID(resp)
	return id, nil
}

// SendRaw is a legacy wrapper for Client.SendRaw.
func SendRaw(pipePath string, data []byte) (uint32, error) {
	client := NewClient(pipePath)
	defer client.Close()
	return client.SendRaw(data)
}

// InputTabRoutingFeature is the CAPABILITIES feature a terminal advertises
// when it delivers INPUT|...|<tab> to that tab. A terminal without it types
// every input into the active tab whatever the request names.
const InputTabRoutingFeature = "input_tab_routing"

// SendKeysTab types text into a tab via INPUT. An index requires input tab
// routing. Use an ID only when the terminal supports authoritative tab IDs.
func SendKeysTab(pipePath, text, tab string) (uint32, error) {
	if !ValidTab(tab) {
		return 0, fmt.Errorf("invalid input tab %q", tab)
	}
	return sendQueued(pipePath, fmt.Sprintf("INPUT|deckpilot|%s|%s", Base64Encode(text), tab))
}

// SendRawTab sends raw bytes (e.g. "\r") to tab via RAW_INPUT.
func SendRawTab(pipePath string, data []byte, tab string) (uint32, error) {
	if !ValidTab(tab) {
		return 0, fmt.Errorf("invalid input tab %q", tab)
	}
	return sendQueued(pipePath, fmt.Sprintf("RAW_INPUT|deckpilot|%s|%s", base64.StdEncoding.EncodeToString(data), tab))
}

func sendQueued(pipePath, msg string) (uint32, error) {
	resp, err := retryBusy(func() (string, error) {
		client := NewClient(pipePath)
		defer client.Close()
		resp, err := client.SendRecv(msg)
		if err != nil {
			return "", err
		}
		if errMsg, ok := IsError(resp); ok {
			return "", fmt.Errorf("server: %s", errMsg)
		}
		return resp, nil
	})
	if err != nil {
		return 0, err
	}
	id, _ := ParseCmdID(resp)
	return id, nil
}

// HasFeature asks the terminal for its CAPABILITIES and reports whether the
// features list names feature. Errors mean "unknown", reported as false.
func HasFeature(pipePath, feature string) bool {
	resp, err := retryBusy(func() (string, error) {
		client := NewClient(pipePath)
		defer client.Close()
		return client.SendRecv("CAPABILITIES")
	})
	if err != nil {
		return false
	}
	for _, field := range strings.Split(strings.TrimSpace(resp), "|") {
		if list, ok := strings.CutPrefix(field, "features="); ok {
			for _, f := range strings.Split(list, ",") {
				if f == feature {
					return true
				}
			}
		}
	}
	return false
}

// SendEnter sends a carriage return via RAW_INPUT.
func SendEnter(pipePath string) error {
	_, err := SendRaw(pipePath, []byte("\r"))
	return err
}

// SendWithSubmit sends text via INPUT, waits for ACK, then sends submitKey
// via RAW_INPUT and waits for ACK. Two separate commands, sequenced with
// drain confirmation between them. Returns the submit cmd_id.
func SendWithSubmit(pipePath, text, submitKey string) (uint32, error) {
	client := NewClient(pipePath)
	defer client.Close()

	// Phase 1: send text via INPUT (if non-empty)
	if text != "" {
		id, err := client.SendKeys(text)
		if err != nil {
			return 0, fmt.Errorf("send text: %w", err)
		}
		// Wait for text to drain
		if id > 0 {
			client.WaitForAck(id)
		}
	}

	// Phase 2: send submit key via RAW_INPUT
	submitID, err := client.SendRaw([]byte(submitKey))
	if err != nil {
		return 0, fmt.Errorf("send submit: %w", err)
	}
	if submitID > 0 {
		client.WaitForAck(submitID)
	}
	return submitID, nil
}

// WaitForAck polls ACK_POLL up to 20 times (2 seconds) with 100ms intervals.
// Returns nil if acked, or error if timed out.
func (c *Client) WaitForAck(cmdID uint32) error {
	if cmdID == 0 {
		return nil
	}
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		acked, err := c.AckPoll(cmdID)
		if err == nil && acked {
			return nil
		}
	}
	return fmt.Errorf("timeout waiting for ACK for cmd_id %d", cmdID)
}

// WaitForAck is a legacy wrapper for Client.WaitForAck.
func WaitForAck(pipePath string, cmdID uint32) error {
	client := NewClient(pipePath)
	defer client.Close()
	return client.WaitForAck(cmdID)
}

// AckPoll sends ACK_POLL|cmd_id and returns true if the cmd was drained.
func (c *Client) AckPoll(cmdID uint32) (bool, error) {
	msg := fmt.Sprintf("ACK_POLL|%d", cmdID)
	resp, err := c.SendRecv(msg)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(resp, "ACK|"), nil
}

// Tail retrieves the last N lines from the terminal buffer.
func (c *Client) Tail(lines int) (string, error) {
	msg := fmt.Sprintf("TAIL|%d", lines)
	resp, err := c.SendRecv(msg)
	if err != nil {
		return "", err
	}
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	return StripTailHeader(resp), nil
}

// Tail is a legacy wrapper for Client.Tail.
func Tail(pipePath string, lines int) (string, error) {
	client := NewClient(pipePath)
	defer client.Close()
	return client.Tail(lines)
}

// History retrieves the full scrollback history.
func (c *Client) History() (string, error) {
	resp, err := c.SendRecv("HISTORY|0")
	if err != nil {
		return "", err
	}
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	return StripTailHeader(resp), nil
}

// History is a legacy wrapper for Client.History.
func History(pipePath string) (string, error) {
	client := NewClient(pipePath)
	defer client.Close()
	return client.History()
}

// ValidTab reports whether tab is a tab target the control plane accepts:
// a decimal index ("0") or a tab id ("id=t_001"). Anything else — notably
// a field containing "|" or a newline — would corrupt the request line.
func ValidTab(tab string) bool {
	if tab == "" {
		return false
	}
	if strings.HasPrefix(tab, "id=") {
		id := tab[3:]
		if id == "" {
			return false
		}
		for _, r := range id {
			if !(r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
				return false
			}
		}
		return true
	}
	for _, r := range tab {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TailTab retrieves the last N lines of a specific tab (index or "id=...").
// Without a tab the server reads the active tab, so a worker in a
// background tab is invisible to plain Tail.
func (c *Client) TailTab(lines int, tab string) (string, error) {
	if !ValidTab(tab) {
		return "", fmt.Errorf("invalid tab %q (want an index like 0 or id=t_001)", tab)
	}
	resp, err := c.SendRecv(fmt.Sprintf("TAIL|%d|%s", lines, tab))
	if err != nil {
		return "", err
	}
	return tabBody(resp, "TAIL|")
}

// tabBody returns the text of a TAIL / HISTORY reply. Anything that is not
// that reply (an ERR, a bare BUSY|..., an unknown line) is an error carrying
// the reply, never an empty screen: an empty string would read as "the tab
// shows nothing".
func tabBody(resp, header string) (string, error) {
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	if !strings.HasPrefix(resp, header) {
		return "", fmt.Errorf("server: %s", firstLine(resp))
	}
	return StripTailHeader(resp), nil
}

// TailTab is a one-shot wrapper for Client.TailTab.
func TailTab(pipePath string, lines int, tab string) (string, error) {
	return retryBusy(func() (string, error) {
		client := NewClient(pipePath)
		defer client.Close()
		return client.TailTab(lines, tab)
	})
}

// HistoryTab retrieves the full scrollback of a specific tab.
func (c *Client) HistoryTab(tab string) (string, error) {
	if !ValidTab(tab) {
		return "", fmt.Errorf("invalid tab %q (want an index like 0 or id=t_001)", tab)
	}
	resp, err := c.SendRecv("HISTORY|0|" + tab)
	if err != nil {
		return "", err
	}
	return tabBody(resp, "HISTORY|")
}

// HistoryTab is a one-shot wrapper for Client.HistoryTab.
func HistoryTab(pipePath, tab string) (string, error) {
	return retryBusy(func() (string, error) {
		client := NewClient(pipePath)
		defer client.Close()
		return client.HistoryTab(tab)
	})
}

// ListTabs returns the raw LIST_TABS reply: a "LIST_TABS|<count>|<active>"
// header followed by one "TAB|<idx>|<id>|..." line per tab.
func (c *Client) ListTabs() (string, error) {
	resp, err := c.SendRecv("LIST_TABS")
	if err != nil {
		return "", err
	}
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	if !strings.HasPrefix(resp, "LIST_TABS|") {
		return "", fmt.Errorf("server: %s", firstLine(resp))
	}
	return resp, nil
}

// ListTabs is a one-shot wrapper for Client.ListTabs.
func ListTabs(pipePath string) (string, error) {
	return retryBusy(func() (string, error) {
		client := NewClient(pipePath)
		defer client.Close()
		return client.ListTabs()
	})
}

// busyRetries / busyBackoff bound retryBusy: about 1.5s in all, enough for a
// renderer lock to clear without making a one-off read feel stuck.
var (
	busyRetries = 5
	busyBackoff = 100 * time.Millisecond
)

// retryBusy repeats a one-shot read while the terminal answers BUSY
// (renderer locked, data lane full). Any other outcome returns at once.
func retryBusy(read func() (string, error)) (string, error) {
	var out string
	var err error
	for attempt := 0; attempt <= busyRetries; attempt++ {
		out, err = read()
		if err == nil || !IsBusy(err.Error()) {
			return out, err
		}
		time.Sleep(time.Duration(attempt+1) * busyBackoff)
	}
	return out, err
}

// TabInList reports whether tab (an index or "id=...") names a tab in a raw
// LIST_TABS reply. The control plane silently falls back to the active tab
// for an unknown index or id, so callers must check before trusting a
// per-tab read.
func TabInList(listReply, tab string) bool {
	id, byID := strings.CutPrefix(tab, "id=")
	for _, ln := range strings.Split(listReply, "\n") {
		f := strings.Split(strings.TrimSpace(ln), "|")
		if len(f) < 3 || f[0] != "TAB" {
			continue
		}
		if byID && f[2] == id {
			return true
		}
		if !byID && f[1] == tab {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// Ping sends a PING command and expects a PONG response.
func (c *Client) Ping() error {
	resp, err := c.SendRecv("PING")
	if err != nil {
		return err
	}
	if errMsg, ok := IsError(resp); ok {
		return fmt.Errorf("server: %s", errMsg)
	}
	if !strings.HasPrefix(resp, PrefixPONG) {
		return fmt.Errorf("unexpected: %s", resp)
	}
	return nil
}

// Ping is a legacy wrapper for Client.Ping.
func Ping(pipePath string) error {
	client := NewClient(pipePath)
	defer client.Close()
	return client.Ping()
}
