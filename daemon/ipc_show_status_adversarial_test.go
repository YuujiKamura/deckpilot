package daemon

import (
	"errors"
	"strings"
	"testing"
)

// TestComposeShowStatus_AlreadyStaleNotDoubled: if a watcher status
// already contains "stale:" (defensive — could happen from cascaded
// fallbacks elsewhere), composeShowStatus must not produce
// "stale:stale:active". The function should detect existing prefix.
//
// Currently this is a documented vulnerability — cascaded "stale:"
// breaks client parsers that strip the prefix once.
func TestComposeShowStatus_AlreadyStaleNotDoubled(t *testing.T) {
	got := composeShowStatus(errors.New("err"), "stale:active")
	if strings.HasPrefix(got, "stale:stale:") {
		t.Errorf("composeShowStatus produced double-prefixed %q — should detect existing stale: marker", got)
	}
}

// TestComposeShowStatus_PipeInjectionInWatcherStatus: the response wire
// format is `OK|<base64>|<status>`. If watcherStatus contains a "|"
// character, the consumer's SplitN(resp, "|", 2) parser breaks because
// status is no longer a single token after the second separator. This is
// a protocol injection attack surface — anything that can influence the
// status field (Watcher internal state) must NOT be allowed to inject
// extra fields.
//
// Currently not defended — composeShowStatus passes the watcher status
// through verbatim. The watcher's status field is set internally by the
// daemon (`active` / `idle` / `busy` / `dead` / `stalled`) so under
// normal use this can't be exploited from outside, but the contract
// should be tight.
func TestComposeShowStatus_PipeInjectionInWatcherStatus(t *testing.T) {
	got := composeShowStatus(nil, "active|injected|payload")
	if strings.Contains(got, "|") {
		t.Errorf("composeShowStatus output %q contains pipe char — would break wire-format parser", got)
	}
}

// TestComposeShowStatus_NewlineInjectionInWatcherStatus: similar attack —
// the daemon response is line-terminated. A newline in status would split
// the response into two lines, breaking the IPC bufio.Scanner reading the
// first line on the client side and possibly truncating real content.
func TestComposeShowStatus_NewlineInjectionInWatcherStatus(t *testing.T) {
	got := composeShowStatus(nil, "active\nMALICIOUS|response")
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("composeShowStatus output %q contains CR/LF — would break line-based wire format", got)
	}
}

// TestComposeShowStatus_NilErrorWithEmptyWatcherStatus: precise behaviour
// pinning — empty status + no fresh error = "unknown", not "stale:" or
// "" (which would emit `OK|<b64>|\n` with empty status field that some
// parsers interpret as success).
func TestComposeShowStatus_NilErrorWithEmptyWatcherStatus(t *testing.T) {
	if got := composeShowStatus(nil, ""); got != "unknown" {
		t.Errorf("composeShowStatus(nil, \"\") = %q, want %q", got, "unknown")
	}
}
