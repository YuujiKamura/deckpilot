package daemon

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// TODO: For production, check the Origin header against your GitHub Pages URL
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// WSMessage represents the JSON structure from the browser (per SKILL-TRAINER-PROPOSAL.md)
type WSMessage struct {
	Cmd     string `json:"cmd"`
	Session string `json:"session,omitempty"`
	Msg     string `json:"msg,omitempty"`     // Base64 encoded for SEND
	Mode    string `json:"mode,omitempty"`    // for SHOW (buffer/history)
	Caller  string `json:"caller,omitempty"`
}

// WSResponse represents the JSON response sent back to the browser
type WSResponse struct {
	Status  string      `json:"status"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// ServeWS starts the WebSocket server on the given address (e.g., ":8080")
func (d *Daemon) ServeWS(addr string) error {
	http.HandleFunc("/ws", d.handleWS)
	log.Printf("daemon: starting WebSocket bridge on %s/ws", addr)
	return http.ListenAndServe(addr, nil)
}

func (d *Daemon) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}
	defer conn.Close()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("ws read error: %v", err)
			break
		}

		var req WSMessage
		if err := json.Unmarshal(message, &req); err != nil {
			d.sendWSError(conn, "Invalid JSON protocol")
			continue
		}

		var resp WSResponse
		switch strings.ToUpper(req.Cmd) {
		case "PING":
			resp = WSResponse{Status: "OK", Message: "PONG"}

		case "LIST":
			sessions := d.listSessions()
			resp = WSResponse{Status: "OK", Data: sessions}

		case "SEND":
			// We reuse the existing logic by simulating the pipe parts
			// parts: ["SEND", name, msgB64, caller]
			parts := []string{"SEND", req.Session, req.Msg, req.Caller}
			out := d.handleSend(parts)
			resp = d.parseIPCResponse(out)

		case "SHOW":
			// parts: ["SHOW", name, mode, caller]
			parts := []string{"SHOW", req.Session, req.Mode, req.Caller}
			out := d.handleShow(parts)
			resp = d.parseIPCResponse(out)

		default:
			resp = WSResponse{Status: "ERR", Message: "Unknown command: " + req.Cmd}
		}

		if err := conn.WriteJSON(resp); err != nil {
			log.Printf("ws write error: %v", err)
			break
		}
	}
}

// parseIPCResponse converts the "OK|..." or "ERR|..." string from handleXxx into a WSResponse
func (d *Daemon) parseIPCResponse(out string) WSResponse {
	out = strings.TrimSuffix(out, "\n")
	parts := strings.SplitN(out, "|", 3)
	if len(parts) < 2 {
		return WSResponse{Status: "ERR", Message: "Internal communication error"}
	}

	status := parts[0]
	if status == "ERR" {
		return WSResponse{Status: "ERR", Message: parts[1]}
	}

	// For SHOW, parts[1] is base64 content, parts[2] is status
	if len(parts) == 3 {
		return WSResponse{
			Status:  "OK",
			Message: parts[2], // status (idle/active)
			Data:    parts[1], // base64 content
		}
	}

	return WSResponse{Status: "OK", Message: parts[1]}
}

func (d *Daemon) sendWSError(conn *websocket.Conn, msg string) {
	conn.WriteJSON(WSResponse{Status: "ERR", Message: msg})
}
