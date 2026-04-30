package pipe

import (
	"bytes"
	"context"
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

	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break // EOF or timeout — done reading
		}
	}
	return strings.TrimRight(buf.String(), "\r\n"), nil
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

// SendEnter sends a carriage return keystroke.
func SendEnter(pipePath string) error {
	return SendKeys(pipePath, "\r")
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
