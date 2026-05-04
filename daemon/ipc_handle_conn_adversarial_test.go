package daemon

import (
	"strings"
	"testing"
)

// TestHandleConnParse_MalformedSend: handleSend extracts parts from
// `SplitN(line, "|", 5)` then accesses parts[1] and parts[2]. With fewer
// fields it must error gracefully rather than panic. This pins the
// length-check contract.
func TestHandleConnParse_MalformedSend(t *testing.T) {
	cases := []struct {
		parts   []string
		wantErr bool
	}{
		{[]string{"SEND"}, true},                     // no name, no msg
		{[]string{"SEND", ""}, true},                 // empty name, no msg
		{[]string{"SEND", "name"}, true},             // name but no msg field at all
		{[]string{"SEND", "name", "not-base64!@#"}, true}, // bad base64
	}
	d := &Daemon{}
	for _, c := range cases {
		got := d.handleSend(c.parts)
		if !strings.HasPrefix(got, "ERR|") {
			if c.wantErr {
				t.Errorf("handleSend(%v) = %q, want ERR| prefix (malformed input)", c.parts, got)
			}
		}
	}
}

// TestHandleConnParse_EmptySessionName: SEND with empty session name string
// must reject — there is no session "" and falling through to pipe lookup
// could leak info or hang on resolution.
func TestHandleConnParse_EmptySessionName(t *testing.T) {
	d := &Daemon{}
	// Empty name, valid base64 of empty msg
	parts := []string{"SEND", "", "", ""}
	got := d.handleSend(parts)
	if !strings.Contains(got, "session not found") && !strings.HasPrefix(got, "ERR|") {
		t.Errorf("handleSend with empty name returned %q — should reject as not found", got)
	}
}

// TestHandleShowParse_MalformedShow: handleShow accesses parts[1..3] for
// name/mode/caller. Verify it does not panic with short slices.
func TestHandleShowParse_MalformedShow(t *testing.T) {
	d := &Daemon{}
	// Just "SHOW" — no name, no caller. Should error cleanly.
	got := d.handleShow([]string{"SHOW"})
	if !strings.HasPrefix(got, "ERR|") {
		t.Errorf("handleShow with no name/caller = %q, want ERR|", got)
	}
}

// TestHandleHookParse_MalformedHook: handleHook(parts) accesses parts[1]
// for the JSON payload. Verify graceful handling of missing payload and
// invalid JSON (which must not be silently accepted).
func TestHandleHookParse_MalformedHook(t *testing.T) {
	d := &Daemon{}
	cases := [][]string{
		{"HOOK"},                // no payload
		{"HOOK", ""},            // empty payload
		{"HOOK", "{not json"},   // invalid JSON
		{"HOOK", "{}"},          // valid JSON but no action
		{"HOOK", `{"action":"unknown_op","hook":{}}`}, // unknown action
	}
	for _, c := range cases {
		got := d.handleHook(c)
		if !strings.HasPrefix(got, "ERR|") {
			t.Errorf("handleHook(%v) = %q, want ERR| prefix", c, got)
		}
	}
}
