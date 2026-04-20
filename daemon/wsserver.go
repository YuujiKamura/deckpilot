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

// WSMessage represents the JSON structure from the browser/client (Issue #27)
type WSMessage struct {
	Cmd     string `json:"cmd"`
	Session string `json:"session,omitempty"`
	From    string `json:"from,omitempty"` // alias for caller
	Msg     string `json:"msg,omitempty"`  // Base64 encoded for INPUT/SEND
	Mode    string `json:"mode,omitempty"` // for SHOW (buffer/history)
	Lines   int    `json:"lines,omitempty"` // for SHOW
	Caller  string `json:"caller,omitempty"` // backward compatibility
}

// WSResponse represents the JSON response sent back to the client
type WSResponse struct {
	Cmd     string      `json:"cmd"`
	Ok      bool        `json:"ok"`
	Error   string      `json:"error,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Status  string      `json:"status,omitempty"` // session status (idle/active/stalled)
	Mode    string      `json:"mode,omitempty"`   // for SHOW
	Message string      `json:"message,omitempty"` // for extra info
}

// ServeWS starts the WebSocket server on the given address (e.g., "127.0.0.1:8080")
func (d *Daemon) ServeWS(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", d.handleWS)
	log.Printf("daemon: starting WebSocket control endpoint on %s/ws", addr)
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return server.ListenAndServe()
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
			// Normal closure or client disconnected
			break
		}

		var req WSMessage
		if err := json.Unmarshal(message, &req); err != nil {
			d.sendWSError(conn, "INPUT", "Invalid JSON protocol")
			continue
		}

		// Normalize fields
		cmd := strings.ToUpper(req.Cmd)
		caller := req.From
		if caller == "" {
			caller = req.Caller
		}

		var resp WSResponse
		resp.Cmd = cmd

		switch cmd {
		case "PING":
			resp.Ok = true
			resp.Message = "PONG"

		case "LIST":
			sessions := d.listSessions()
			resp.Ok = true
			resp.Data = sessions

		case "INPUT", "SEND":
			// parts: ["SEND", name, msgB64, caller]
			parts := []string{"SEND", req.Session, req.Msg, caller}
			out := d.handleSend(parts)
			resp = d.parseIPCResponse(out)
			resp.Cmd = cmd

		case "SHOW":
			// parts: ["SHOW", name, mode, caller]
			mode := req.Mode
			if mode == "" {
				mode = "buffer"
			}
			parts := []string{"SHOW", req.Session, mode, caller}
			out := d.handleShow(parts)
			resp = d.parseIPCResponse(out)
			resp.Cmd = cmd
			resp.Mode = mode

		case "STATE":
			w, ok := d.getWatcher(req.Session)
			if !ok {
				d.refreshSessions()
				w, ok = d.getWatcher(req.Session)
			}
			if !ok {
				resp.Ok = false
				resp.Error = "session not found: " + req.Session
			} else {
				resp.Ok = true
				resp.Status = w.Status()
			}

		default:
			resp.Ok = false
			resp.Error = "unknown cmd: " + req.Cmd
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
	
	var resp WSResponse
	if len(parts) < 2 {
		resp.Ok = false
		resp.Error = "Internal communication error"
		return resp
	}

	status := parts[0]
	if status == "ERR" {
		resp.Ok = false
		resp.Error = parts[1]
		return resp
	}

	resp.Ok = true
	// For SHOW, parts[1] is base64 content, parts[2] is status
	if len(parts) == 3 {
		resp.Data = parts[1]   // base64 content
		resp.Status = parts[2] // status (idle/active)
	} else {
		resp.Message = parts[1]
	}

	return resp
}

func (d *Daemon) sendWSError(conn *websocket.Conn, cmd, msg string) {
	conn.WriteJSON(WSResponse{Cmd: cmd, Ok: false, Error: msg})
}
