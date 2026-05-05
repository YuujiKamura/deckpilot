package pipe

import (
	"strings"
	"testing"
)

// TestParseGhosttyResponse_TolerantOfUnknownPrefix pins forward-compat
// for unrecognised wire-format prefixes the ghostty pipe server may
// emit in future releases. Concretely: ghostty's
// feat/cp-busy-mitigation branch added stale-marker prefixes of the
// form `stale:<age_ms>|<payload>` on TAIL/HISTORY responses while the
// renderer was locked. Older versions of this codebase had no defence
// against such prefixes — IsError would miss the inner ERR|, and
// StripTailHeader would strip the wrong line, returning garbled buffer
// content to the user.
//
// The contract this test pins is INTENTIONALLY MINIMAL: it does NOT
// require the parser to *understand* unknown prefixes (that would
// over-fit to one specific ghostty version). It only requires that
// neither IsError, IsBusy, ClassifyBusy nor StripTailHeader panic on
// such inputs. A graceful "do nothing / classify as no-op" is the
// minimum bar; explicit recognition of `stale:` is a nice-to-have but
// out of scope for this regression pin.
//
// If a future version of ghostty adds e.g. `degraded:foo|...` or
// `mitigated:N|...` prefixes, this test catches the case where the
// daemon would crash before the protocol layer even gets a chance to
// classify it.
func TestParseGhosttyResponse_TolerantOfUnknownPrefix(t *testing.T) {
	cases := []string{
		// stale:<age_ms>|<payload> — ghostty feat/cp-busy-mitigation
		"stale:1234|TAIL|sess|50\nbuffer line 1\nbuffer line 2",
		"stale:0|ERR|BUSY|renderer_locked",
		"stale:9999999999999999999|PONG|",
		// hypothetical future prefixes
		"degraded:rendering|TAIL|sess|10\nfoo",
		"mitigated:5|HISTORY|sess|0\nbar",
		"future_marker:N|payload",
		"weird_prefix|payload", // no colon, not a wire-format token
		// adversarial: prefix-only, no payload
		"stale:1234|",
		"stale:|",
		"|",
		// empty / whitespace
		"",
		"   ",
		"\n",
		"\r\n",
	}

	for _, c := range cases {
		t.Run(strings.ReplaceAll(c, "\n", "\\n"), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on input %q: %v", c, r)
				}
			}()

			// All four parser entry points must tolerate the input.
			// We do not assert specific outputs — the contract is
			// "no crash, deterministic answer".
			_, _ = IsError(c)
			_ = IsBusy(c)
			_ = ClassifyBusy(c)
			_ = StripTailHeader(c)
			_ = IsNoTabs(c)
		})
	}
}

// TestStripTailHeader_NoCrashOnMalformedInput is the focused
// no-panic guard for StripTailHeader specifically. The function uses
// strings.Index on '\n' and slices from idx+1; if a future bug
// reintroduces an unchecked slice (idx-1 instead of idx+1, or a wrong
// bounds check), this test catches it before it ships.
func TestStripTailHeader_NoCrashOnMalformedInput(t *testing.T) {
	cases := []struct {
		in          string
		wantContent string // empty if no \n present
	}{
		{"TAIL|sess|0\n", ""},
		{"TAIL|sess|2\nline1\nline2", "line1\nline2"},
		{"no newline at all", ""},
		{"", ""},
		{"\n", ""},
		{"\nbody only", "body only"},
		// stale-prefixed: StripTailHeader only knows about the
		// FIRST newline, so the stale prefix line is treated as the
		// "header" and stripped together with TAIL|. This is OK
		// (and is what we want for forward-compat) — assert it.
		{"stale:1234|TAIL|sess|2\nline1\nline2", "line1\nline2"},
	}
	for _, c := range cases {
		got := StripTailHeader(c.in)
		if got != c.wantContent {
			t.Errorf("StripTailHeader(%q) = %q, want %q", c.in, got, c.wantContent)
		}
	}
}

// TestIsError_DoesNotMisclassifyStalePrefixedError pins a subtle
// failure mode for the inbound stale-prefix case. IsError checks for
// the literal "ERR|" prefix after TrimSpace; it does NOT walk through
// arbitrary leading prefixes. So a future ghostty response of the form
// `stale:1234|ERR|...` will be classified as a NON-error response by
// IsError. That is the documented current behavior — this test pins it
// so that any future change which DOES try to be clever (and gets it
// half-right) is caught immediately.
//
// If we ever want IsError to recognise stale-wrapped errors, this test
// must be updated alongside the new behavior, with a clear migration
// note.
func TestIsError_DoesNotMisclassifyStalePrefixedError(t *testing.T) {
	cases := []struct {
		in        string
		wantIsErr bool
	}{
		{"ERR|something", true},                        // canonical
		{"stale:1234|ERR|something", false},            // stale-wrapped: not currently recognised
		{"stale:0|something", false},                   // stale, no inner ERR
		{"  stale:1234|ERR|something  ", false},        // even after TrimSpace, leading prefix breaks ERR| match
	}
	for _, c := range cases {
		_, gotIsErr := IsError(c.in)
		if gotIsErr != c.wantIsErr {
			t.Errorf("IsError(%q) isErr = %v, want %v (current contract; update both sides if behavior changes)",
				c.in, gotIsErr, c.wantIsErr)
		}
	}
}
