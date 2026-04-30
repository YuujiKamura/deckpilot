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

		// For TAIL responses, there may be more data (content lines).
		if strings.HasPrefix(resp, "TAIL|") {
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
