package pipe

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Protocol prefixes
const (
	PrefixERR  = "ERR|"
	PrefixTAIL = "TAIL|"
	PrefixPONG = "PONG|"
)

// Base64Encode encodes text to base64.
func Base64Encode(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}

// Base64Decode decodes a base64 string.
func Base64Decode(encoded string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// IsError checks if the response has an ERR| prefix.
// Returns the error message and true if it is an error response.
func IsError(resp string) (string, bool) {
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, PrefixERR) {
		return strings.TrimPrefix(resp, PrefixERR), true
	}
	return "", false
}

// BusyKind classifies a Ghostty pipe-server response for backpressure-related
// conditions. These are *transient* — distinct from session-fatal errors like
// NO_TABS, which mark the session dead.
type BusyKind int

const (
	BusyNone BusyKind = iota
	BusyRendererLocked
	BusyDataLaneFull
	BusyUIThreadBusy
	BusyOther // BUSY|... or TIMEOUT|... we have not seen before
)

// stripEnvelopePrefixes normalises any envelope wrapping the deckpilot
// stack might apply (raw protocol `ERR|...`, server-wrapped `server: ...`,
// nested combinations of either, in any order) into a bare protocol-token
// body (e.g. `BUSY|...`, `TIMEOUT|...`, `NO_TABS`) suitable for prefix
// inspection. Loops until no more known prefix matches so callers do not
// have to know the wrap depth or order; this defends against legacy
// clients, double-wrapping proxies, and future pipe-server changes.
func stripEnvelopePrefixes(s string) string {
	s = strings.TrimSpace(s)
	for {
		before := s
		s = strings.TrimPrefix(s, "server: ")
		s = strings.TrimPrefix(s, PrefixERR)
		if s == before {
			return s
		}
	}
}

// IsBusy reports whether the input represents a transient backpressure
// condition from the pipe server. Inputs may be raw protocol responses,
// stripped error messages, or wrapped error strings. Session-fatal errors
// (NO_TABS) and unrelated errors return false.
func IsBusy(s string) bool {
	return ClassifyBusy(s) != BusyNone
}

// IsNoTabs reports whether the input represents the session-fatal
// NO_TABS signal from the pipe server (terminal has no tabs, session
// ended). The match is token-boundary aware: `NO_TABS_ALLOWED` or
// arbitrary message text containing the substring NO_TABS does NOT
// trigger — only the standalone protocol token does. Accepts the same
// envelope variants as IsBusy.
func IsNoTabs(s string) bool {
	body := stripEnvelopePrefixes(s)
	const tok = "NO_TABS"
	if !strings.HasPrefix(body, tok) {
		return false
	}
	rest := body[len(tok):]
	if rest == "" {
		return true
	}
	// Token continues if the next byte is part of the same identifier.
	c := rest[0]
	isWordChar := (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
	return !isWordChar
}

// ClassifyBusy maps an error/response string to a BusyKind. Returns BusyNone
// for non-busy strings.
func ClassifyBusy(s string) BusyKind {
	body := stripEnvelopePrefixes(s)
	switch {
	case strings.HasPrefix(body, "BUSY|renderer_locked"):
		return BusyRendererLocked
	case strings.HasPrefix(body, "BUSY|data_lane_full"):
		return BusyDataLaneFull
	case strings.HasPrefix(body, "TIMEOUT|ui_thread_busy"):
		return BusyUIThreadBusy
	case strings.HasPrefix(body, "BUSY|") || strings.HasPrefix(body, "TIMEOUT|"):
		return BusyOther
	}
	return BusyNone
}

// ParseCmdID extracts cmd_id from QUEUED|session|type|cmd_id response.
func ParseCmdID(resp string) (uint32, bool) {
	parts := strings.Split(strings.TrimSpace(resp), "|")
	if len(parts) >= 4 && parts[0] == "QUEUED" {
		var id uint32
		_, err := fmt.Sscanf(parts[3], "%d", &id)
		if err == nil {
			return id, true
		}
	}
	return 0, false
}

// AckPoll sends ACK_POLL|cmd_id and returns true if the cmd was drained.
func AckPoll(pipePath string, cmdID uint32) (bool, error) {
	msg := fmt.Sprintf("ACK_POLL|%d", cmdID)
	resp, err := SendRecv(pipePath, msg)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(resp, "ACK|"), nil
}

// StripTailHeader strips the first line (TAIL|<session>|<linecount>)
// from a multi-line response and returns the remaining content.
func StripTailHeader(resp string) string {
	idx := strings.Index(resp, "\n")
	if idx < 0 {
		return ""
	}
	return resp[idx+1:]
}
