package daemon

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWSServer_RoundTrip(t *testing.T) {
	d := New()
	// Mock some sessions
	d.mu.Lock()
	d.sessions["test-session"] = "mock-pipe"
	d.mu.Unlock()
	// We don't start actual watchers, so listSessions will return unknown status

	server := httptest.NewServer(http.HandlerFunc(d.handleWS))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http", "ws", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	t.Run("PING", func(t *testing.T) {
		req := WSMessage{Cmd: "PING"}
		if err := conn.WriteJSON(req); err != nil {
			t.Fatalf("WriteJSON failed: %v", err)
		}

		var resp WSResponse
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("ReadJSON failed: %v", err)
		}
		if resp.Cmd != "PING" || !resp.Ok || resp.Message != "PONG" {
			t.Errorf("Unexpected PING response: %+v", resp)
		}
	})

	t.Run("LIST", func(t *testing.T) {
		req := WSMessage{Cmd: "LIST"}
		if err := conn.WriteJSON(req); err != nil {
			t.Fatalf("WriteJSON failed: %v", err)
		}

		var resp WSResponse
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("ReadJSON failed: %v", err)
		}
		if resp.Cmd != "LIST" || !resp.Ok {
			t.Errorf("Unexpected LIST response: %+v", resp)
		}
		data, ok := resp.Data.([]interface{})
		if !ok || len(data) == 0 {
			t.Errorf("Expected session list in Data, got %+v", resp.Data)
		}
	})

	t.Run("STATE_NotFound", func(t *testing.T) {
		req := WSMessage{Cmd: "STATE", Session: "nosuch"}
		if err := conn.WriteJSON(req); err != nil {
			t.Fatalf("WriteJSON failed: %v", err)
		}

		var resp WSResponse
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("ReadJSON failed: %v", err)
		}
		if resp.Ok || !strings.Contains(resp.Error, "not found") {
			t.Errorf("Expected error for missing session, got %+v", resp)
		}
	})

	t.Run("UnknownCommand", func(t *testing.T) {
		req := WSMessage{Cmd: "FOO"}
		if err := conn.WriteJSON(req); err != nil {
			t.Fatalf("WriteJSON failed: %v", err)
		}

		var resp WSResponse
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("ReadJSON failed: %v", err)
		}
		if resp.Ok || !strings.Contains(resp.Error, "unknown cmd") {
			t.Errorf("Expected error for unknown cmd, got %+v", resp)
		}
	})
}

// TestWS_INPUT_round_trip verifies the INPUT command flows through the WebSocket
// dispatcher and that a missing session produces a well-formed Ok/Error envelope.
// The happy path requires a live Windows named pipe + watcher goroutine and is
// covered by integration / manual testing — here we lock in the protocol shape
// (cmd echo, Ok=false, Error contains "session not found") so regressions in the
// envelope mapping get caught.
func TestWS_INPUT_round_trip(t *testing.T) {
	d := New()
	server := httptest.NewServer(http.HandlerFunc(d.handleWS))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http", "ws", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	payload := base64.StdEncoding.EncodeToString([]byte("hello"))
	req := WSMessage{Cmd: "INPUT", Session: "nonexistent-session", Msg: payload, From: "unit-test"}
	if err := conn.WriteJSON(req); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	var resp WSResponse
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}
	if resp.Cmd != "INPUT" {
		t.Errorf("expected Cmd=INPUT, got %q", resp.Cmd)
	}
	if resp.Ok {
		t.Errorf("expected Ok=false for missing session, got %+v", resp)
	}
	if !strings.Contains(resp.Error, "session not found") {
		t.Errorf("expected Error to mention 'session not found', got %q", resp.Error)
	}
}

// TestWS_SHOW_round_trip exercises the SHOW command happy path by installing a
// mock watcher whose Run loop is not started. We mark its status as "dead" so
// FreshTail short-circuits to LastContent(), avoiding the pipe goroutine. The
// test verifies the WS envelope (Ok=true, Data=base64, Status propagated, Mode
// echoed).
func TestWS_SHOW_round_trip(t *testing.T) {
	d := New()

	sess := "test-show-session"
	w := NewWatcher(sess, "fake-pipe", "fake-file", 0, "", nil)
	// Mark as dead so FreshTail returns cached LastContent without talking to the
	// (nonexistent) pipe goroutine.
	w.mu.Lock()
	w.status = "dead"
	w.lastContent = "mock buffer content"
	w.mu.Unlock()

	d.mu.Lock()
	d.sessions[sess] = "fake-pipe"
	d.watchers[sess] = w
	d.mu.Unlock()

	server := httptest.NewServer(http.HandlerFunc(d.handleWS))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http", "ws", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	req := WSMessage{Cmd: "SHOW", Session: sess, Mode: "buffer", From: "unit-test"}
	if err := conn.WriteJSON(req); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	var resp WSResponse
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}
	if resp.Cmd != "SHOW" {
		t.Errorf("expected Cmd=SHOW, got %q", resp.Cmd)
	}
	if !resp.Ok {
		t.Fatalf("expected Ok=true, got %+v", resp)
	}
	if resp.Mode != "buffer" {
		t.Errorf("expected Mode=buffer, got %q", resp.Mode)
	}
	if resp.Status != "dead" {
		t.Errorf("expected Status=dead (watcher state propagated), got %q", resp.Status)
	}
	dataStr, ok := resp.Data.(string)
	if !ok {
		t.Fatalf("expected Data to be base64 string, got %T: %+v", resp.Data, resp.Data)
	}
	decoded, err := base64.StdEncoding.DecodeString(dataStr)
	if err != nil {
		t.Fatalf("Data is not valid base64: %v", err)
	}
	if string(decoded) != "mock buffer content" {
		t.Errorf("expected decoded content %q, got %q", "mock buffer content", string(decoded))
	}
}
