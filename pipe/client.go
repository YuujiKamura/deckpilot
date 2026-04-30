package pipe

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	winio "github.com/Microsoft/go-winio"
)

const dialTimeout = 2 * time.Second

// SendRecv performs a one-shot named pipe exchange: dial, write message
// (appends \n if missing), read one response line, close.
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
	if _, err := conn.Write([]byte(message)); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	// Read all available lines into a single response
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return strings.Join(lines, "\n"), nil
}

// SendKeys sends base64-encoded keystrokes via the INPUT command.
func SendKeys(pipePath, text string) error {
	msg := fmt.Sprintf("INPUT|deckpilot|%s", Base64Encode(text))
	resp, err := SendRecv(pipePath, msg)
	if err != nil {
		return err
	}
	if errMsg, ok := IsError(resp); ok {
		return fmt.Errorf("server error: %s", errMsg)
	}
	return nil
}

// SendEnter sends a carriage return keystroke.
func SendEnter(pipePath string) error {
	return SendKeys(pipePath, "\r")
}

// Tail retrieves the last N lines from the terminal buffer.
// Response format: TAIL|<session>|<linecount>\n<content lines>
func Tail(pipePath string, lines int) (string, error) {
	msg := fmt.Sprintf("TAIL|%d", lines)
	resp, err := SendRecv(pipePath, msg)
	if err != nil {
		return "", err
	}
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server error: %s", errMsg)
	}
	return StripTailHeader(resp), nil
}

// History retrieves the full scrollback history.
// Sends HISTORY|0, same response format as TAIL.
func History(pipePath string) (string, error) {
	resp, err := SendRecv(pipePath, "HISTORY|0")
	if err != nil {
		return "", err
	}
	if errMsg, ok := IsError(resp); ok {
		return "", fmt.Errorf("server error: %s", errMsg)
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
		return fmt.Errorf("server error: %s", errMsg)
	}
	if !strings.HasPrefix(resp, PrefixPONG) {
		return fmt.Errorf("unexpected response: %s", resp)
	}
	return nil
}
