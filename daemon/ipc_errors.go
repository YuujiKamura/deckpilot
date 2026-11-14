package daemon

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSessionNotFound is the sentinel for "the daemon does not know this
// session". It is a permanent condition for a monitor bound to that session:
// the daemon already re-scans sessions (refreshSessions) before answering
// not-found, so polling again cannot make the session reappear. Callers test
// for it with errors.Is instead of matching message text.
var ErrSessionNotFound = errors.New("session not found")

// sessionNotFoundWire is the text the IPC server writes after "ERR|" for a
// missing session. Server and client share this constant so the wire format
// and the client-side classification cannot drift apart.
const sessionNotFoundWire = "session not found: "

// sessionNotFoundResp builds the IPC error line for a missing session.
func sessionNotFoundResp(name string) string {
	return "ERR|" + sessionNotFoundWire + name + "\n"
}

// SessionNotFound returns an error for name that matches ErrSessionNotFound
// under errors.Is. Its message is "session not found: <name>", unchanged from
// the historical text so existing log greps keep working.
func SessionNotFound(name string) error {
	return fmt.Errorf("%w: %s", ErrSessionNotFound, name)
}

// errFromResponse converts the body of an "ERR|<body>" IPC response into an
// error. A missing-session body becomes SessionNotFound (errors.Is-able);
// every other body is returned as plain text, as before. The text protocol
// leaves no other place to classify, so this is the single point that reads
// the wire string.
func errFromResponse(body string) error {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, sessionNotFoundWire) {
		return SessionNotFound(strings.TrimPrefix(body, sessionNotFoundWire))
	}
	return errors.New(body)
}
