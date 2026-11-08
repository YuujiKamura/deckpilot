package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type NotificationEvent struct {
	CreatedAt time.Time `json:"created_at"`
	Recipient string    `json:"recipient"`
	Session   string    `json:"session"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
}

var notificationInboxMu sync.Mutex

func notificationInboxPath() string {
	home, err := daemonUserHome()
	if err == nil {
		return filepath.Join(home, ".deckpilot", "notifications.jsonl")
	}
	return filepath.Join(os.TempDir(), "deckpilot", "notifications.jsonl")
}

func appendNotificationEvent(event NotificationEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	path := notificationInboxPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	notificationInboxMu.Lock()
	defer notificationInboxMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return err
	}
	return nil
}

func ReadNotificationEvents(limit int) ([]NotificationEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	f, err := os.Open(notificationInboxPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var events []NotificationEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		var event NotificationEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue // ignore a partial final record after an interrupted write
		}
		events = append(events, event)
		if len(events) > limit {
			events = events[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func callbackNotificationMessage(notification BufferNotification, custom string) string {
	if custom != "" {
		return strings.TrimSpace(custom)
	}
	verb := notification.Status
	if verb == "stalled" {
		verb = "hung"
	}
	return fmt.Sprintf("Session %s is %s", notification.SessionName, verb)
}
