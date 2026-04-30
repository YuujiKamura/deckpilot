package daemon

import (
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
