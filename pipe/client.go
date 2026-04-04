package pipe

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	winio "github.com/Microsoft/go-winio"
)

const dialTimeout = 2 * time.Second
const readDeadline = 5 * time.Second

// SendRecv performs a one-shot named pipe exchange: dial, write message,
// read response until EOF, close.
func SendRecv(pipePath, message string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	conn, err := winio.DialPipeContext(ctx, pipePath)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", pipePath, err)
	}
	defer conn.Close()

	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	conn.SetDeadline(time.Now().Add(readDeadline))
	if _, err := conn.Write([]byte(message)); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	// Read first response.
	tmp := make([]byte, 65536)
	n, err := conn.Read(tmp)
	if err != nil && n == 0 {
		return "", fmt.Errorf("read: %w", err)
	}
	resp := strings.TrimRight(string(tmp[:n]), "\r\n")

	// For TAIL responses, there may be more data (content lines).
	if strings.HasPrefix(resp, "TAIL|") {
		// Read remaining content until EOF or timeout
		var buf bytes.Buffer
		buf.WriteString(string(tmp[:n]))
		for {
			nn, err := conn.Read(tmp)
			if nn > 0 {
				buf.Write(tmp[:nn])
			}
			if err != nil {
				break
			}
		}
		return strings.TrimRight(buf.String(), "\r\n"), nil
	}

	return resp, nil
}

// SendKeys sends base64-encoded keystrokes via the INPUT command.
func SendKeys(pipePath, text string) error {
	msg := fmt.Sprintf("INPUT|deckpilot|%s", Base64Encode(text))
	resp, err := SendRecv(pipePath, msg)
	if err != nil {
		return fmt.Errorf("sendkeys: %w", err)
	}
	if errMsg, ok := IsError(resp); ok {
		return fmt.Errorf("server: %s", errMsg)
	}
	return nil
}

// SendRaw sends raw bytes via RAW_INPUT (not interpreted as text).
// Falls back to INPUT if RAW_INPUT is not supported.
func SendRaw(pipePath string, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	msg := fmt.Sprintf("RAW_INPUT|deckpilot|%s", encoded)
	resp, err := SendRecv(pipePath, msg)
	if err != nil {
		return fmt.Errorf("sendraw: %w", err)
	}
	if errMsg, ok := IsError(resp); ok {
		// Fallback to INPUT if server doesn't support RAW_INPUT
		if strings.Contains(errMsg, "PARSE_ERROR") || strings.Contains(errMsg, "unknown") {
			return SendKeys(pipePath, string(data))
		}
		return fmt.Errorf("server: %s", errMsg)
	}
	return nil
}

// SendEnter sends a carriage return via RAW_INPUT.
func SendEnter(pipePath string) error {
	return SendRaw(pipePath, []byte("\r"))
}

// SendWithSubmit sends text via INPUT, waits for ACK, then sends submitKey
// via RAW_INPUT and waits for ACK. Two separate commands, sequenced with
// drain confirmation between them. Returns the submit cmd_id.
func SendWithSubmit(pipePath, text, submitKey string) (uint32, error) {
	// Phase 1: send text via INPUT (if non-empty)
	if text != "" {
		resp, err := SendRecv(pipePath, fmt.Sprintf("INPUT|deckpilot|%s", Base64Encode(text)))
		if err != nil {
			return 0, fmt.Errorf("send text: %w", err)
		}
		if errMsg, ok := IsError(resp); ok {
			return 0, fmt.Errorf("text: %s", errMsg)
		}
		// Wait for text to drain
		if textID, ok := ParseCmdID(resp); ok {
			waitForAck(pipePath, textID)
		}
	}

	// Phase 2: send submit key via RAW_INPUT
	encoded := base64.StdEncoding.EncodeToString([]byte(submitKey))
	resp, err := SendRecv(pipePath, fmt.Sprintf("RAW_INPUT|deckpilot|%s", encoded))
	if err != nil {
		return 0, fmt.Errorf("send submit: %w", err)
	}
	if errMsg, ok := IsError(resp); ok {
		return 0, fmt.Errorf("submit: %s", errMsg)
	}
	submitID, _ := ParseCmdID(resp)
	if submitID > 0 {
		waitForAck(pipePath, submitID)
	}
	return submitID, nil
}

// waitForAck polls ACK_POLL up to 10 times with 100ms intervals.
func waitForAck(pipePath string, cmdID uint32) {
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		acked, err := AckPoll(pipePath, cmdID)
		if err == nil && acked {
			return
		}
	}
}

// Tail retrieves the last N lines from the terminal buffer.
func Tail(pipePath string, lines int) (string, error) {
	msg := fmt.Sprintf("TAIL|%d", lines)
	resp, err := SendRecv(pipePath, msg)
	if err != nil {
		return "", err
	}
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	return StripTailHeader(resp), nil
}

// History retrieves the full scrollback history.
func History(pipePath string) (string, error) {
	resp, err := SendRecv(pipePath, "HISTORY|0")
	if err != nil {
		return "", err
	}
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	return StripTailHeader(resp), nil
}

// Ping sends a PING command and expects a PONG response.
func Ping(pipePath string) error {
	resp, err := SendRecv(pipePath, "PING")
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
