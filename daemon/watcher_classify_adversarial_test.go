package daemon

import (
	"errors"
	"testing"
)

// TestClassifyTailError_NoTabsSubstringFalsePositive: a non-fatal error
// containing the literal "NO_TABS" as substring (e.g. quoted in a longer
// diagnostic message) must not be classified as session-fatal. Currently
// the classifier uses strings.Contains which is too permissive — if a
// future ghostty release ever embeds "NO_TABS" in an explanatory error
// (e.g. "missing tabs (was NO_TABS earlier)"), every session would be
// killed instantly.
func TestClassifyTailError_NoTabsSubstringFalsePositive(t *testing.T) {
	cases := []string{
		"server: error not related to NO_TABS at all",
		"diagnostic: previously saw NO_TABS but recovered",
		"NO_TABS_ALLOWED in this build",
	}
	for _, s := range cases {
		got := classifyTailError(errors.New(s))
		if got.MarkDead {
			t.Errorf("classifyTailError(%q) marked dead, but NO_TABS appears as substring not as the actual session-end signal", s)
		}
	}
}

// TestClassifyTailError_PrecedenceLeadingTokenWins: classification respects
// protocol-token order — whichever recognised token leads the (envelope-
// stripped) body wins. NO_TABS as the leading token marks dead even if
// trailing junk includes a BUSY-looking string; BUSY as the leading token
// marks busy even with NO_TABS appearing later as substring (which the
// stricter IsNoTabs no longer treats as session-fatal). Pins the
// precedence rule so a careless future change cannot silently flip it.
func TestClassifyTailError_PrecedenceLeadingTokenWins(t *testing.T) {
	cases := []struct {
		in      string
		want    tailErrorClassification
		comment string
	}{
		{
			in:      "server: NO_TABS extra context BUSY",
			want:    tailErrorClassification{MarkDead: true},
			comment: "NO_TABS leads → session fatal even with BUSY in trailing text",
		},
		{
			in:      "server: BUSY|renderer_locked NO_TABS",
			want:    tailErrorClassification{NewStatus: "busy"},
			comment: "BUSY leads → transient even with NO_TABS as substring later",
		},
		{
			in:      "server: NO_TABS|extra_field",
			want:    tailErrorClassification{MarkDead: true},
			comment: "NO_TABS with pipe-delimited continuation still session-fatal",
		},
	}
	for _, c := range cases {
		got := classifyTailError(errors.New(c.in))
		if got != c.want {
			t.Errorf("classifyTailError(%q): got %+v, want %+v (%s)", c.in, got, c.want, c.comment)
		}
	}
}

// TestClassifyTailError_BusyControlCharsNotMisclassified: BUSY-class error
// with embedded CR/LF/NUL/ANSI-escape — a malicious or buggy ghostty server
// could craft these. The classifier must still recognise the BUSY token
// based on prefix, not be fooled into treating the whole as transport.
func TestClassifyTailError_BusyControlCharsNotMisclassified(t *testing.T) {
	cases := []string{
		"server: BUSY|renderer_locked\r\nextra junk",
		"server: BUSY|renderer_locked\x1b[31mansi\x1b[0m",
		"server: BUSY|renderer_locked\x00null",
	}
	for _, s := range cases {
		got := classifyTailError(errors.New(s))
		if got.NewStatus != "busy" {
			t.Errorf("classifyTailError(%q): expected NewStatus=busy, got %+v", s, got)
		}
		if got.AdvanceRetry {
			t.Errorf("classifyTailError(%q): should not advance dead retry, got %+v", s, got)
		}
	}
}
