package daemon

// This file gathers the daemon package's domain / value types in one place so
// daemon.go and watcher.go can focus on lifecycle and orchestration.
//
// Intentionally unexported types stay unexported (e.g. sessionInfo) — the
// move is cohesion-only, not an API widening.

// IdleHook defines an action to execute when a session becomes idle.
type IdleHook struct {
	ID              string            `json:"id,omitempty"`     // stable identifier; used as the on-disk filename for persistence
	Type            string            `json:"type"`             // "http", "stdout", "send", "callback"
	URL             string            `json:"url"`              // for "http" type
	Method          string            `json:"method"`           // for "http" type (default: POST)
	Headers         map[string]string `json:"headers"`          // for "http" type
	Message         string            `json:"message"`          // for "stdout" and "send" types
	TargetSession   string            `json:"target_session"`   // for "send" type
	SessionFilter   string            `json:"session_filter"`   // optional: only trigger for specific sessions
	CallbackSession string            `json:"callback_session"` // for "callback" type - session to notify
	OneTime         bool              `json:"one_time"`         // if true, remove hook after execution
}

// sessionInfo is the JSON shape returned by the daemon's LIST IPC. Kept
// unexported — clients import cmd's listEntry mirror instead.
type sessionInfo struct {
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	PipePath   string `json:"pipe_path"`
	WsURL      string `json:"ws_url,omitempty"`
	AppRuntime string `json:"app_runtime"`
	Status     string `json:"status"`
	Uptime     string `json:"uptime"`
	Protected  bool   `json:"protected"`
}

// BufferNotification is emitted when a watcher detects a change or stability.
type BufferNotification struct {
	SessionName   string
	Content       string
	Hash          string
	Changed       bool
	StableFor     int
	Status        string
	StatusChanged bool   // true if status changed from previous poll
	PrevStatus    string // previous status before change
}
