package cmd

import (
	"strings"
	"testing"
)

// rule builds a horizontal rule line of n box-drawing chars.
func rule(n int) string { return strings.Repeat(string(ruleRune), n) }

func TestIsRuleLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"exactly minRuleLen", rule(minRuleLen), true},
		{"one below minRuleLen", rule(minRuleLen - 1), false},
		{"long full-width rule", rule(89), true},
		{"contains a space", rule(10) + " " + rule(10), false},
		{"contains a prompt char", rule(20) + ">", false},
		{"ascii dashes are not rules", strings.Repeat("-", 40), false},
		{"empty line", "", false},
		{"trailing CR tolerated", rule(30) + "\r", true},
		{"surrounding whitespace tolerated", "  " + rule(30) + "  ", true},
		{"plain text line", "> some text", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRuleLine(tt.line); got != tt.want {
				t.Errorf("isRuleLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestIsPromptLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"> hello", true},
		{">", true},
		{"  > indented", true},
		{"> \r", true},
		{"not a prompt", false},
		{"  hello >", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isPromptLine(tt.line); got != tt.want {
			t.Errorf("isPromptLine(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestMarkInputBox_HappyNonEmpty(t *testing.T) {
	content := strings.Join([]string{
		"some transcript output",
		rule(40),
		"> push して",
		rule(40),
		"  [statusline] idle",
	}, "\n")

	marked, summary := markInputBox(content)

	if summary != "push して" {
		t.Errorf("summary = %q, want %q", summary, "push して")
	}
	wantLine := "> " + inputBoxMarker + " push して"
	if !strings.Contains(marked, wantLine) {
		t.Errorf("marked buffer missing %q\ngot:\n%s", wantLine, marked)
	}
	// The bare prompt line must no longer be present.
	if strings.Contains(marked, "\n> push して\n") {
		t.Errorf("unmarked prompt line still present:\n%s", marked)
	}
}

func TestMarkInputBox_HappyEmpty(t *testing.T) {
	content := strings.Join([]string{
		rule(40),
		"> ",
		rule(40),
	}, "\n")

	marked, summary := markInputBox(content)

	if summary != "(空)" {
		t.Errorf("summary = %q, want %q", summary, "(空)")
	}
	if !strings.Contains(marked, "> "+inputBoxEmptyMarker) {
		t.Errorf("empty-box marker missing\ngot:\n%s", marked)
	}
}

func TestMarkInputBox_NoRules(t *testing.T) {
	content := "just\nsome\nplain\ntext"
	marked, summary := markInputBox(content)
	if marked != content {
		t.Errorf("content changed without rules:\n%s", marked)
	}
	if summary != "" {
		t.Errorf("summary = %q, want empty", summary)
	}
}

func TestMarkInputBox_SingleRule(t *testing.T) {
	content := "header\n" + rule(40) + "\n> orphan"
	marked, summary := markInputBox(content)
	if marked != content {
		t.Errorf("content changed with a single rule:\n%s", marked)
	}
	if summary != "" {
		t.Errorf("summary = %q, want empty", summary)
	}
}

func TestMarkInputBox_RulePairButNoPrompt(t *testing.T) {
	// Two rules wrapping a non-prompt line — e.g. a tool-output divider block.
	content := strings.Join([]string{
		rule(40),
		"  Files changed: 3",
		rule(40),
	}, "\n")
	marked, summary := markInputBox(content)
	if marked != content {
		t.Errorf("content changed for a non-prompt ruled block:\n%s", marked)
	}
	if summary != "" {
		t.Errorf("summary = %q, want empty", summary)
	}
}

func TestMarkInputBox_FakeBoxAboveRealBox(t *testing.T) {
	// A decoy box appears earlier in the transcript; the real live input box
	// is the bottom-most one and only it must be tagged.
	content := strings.Join([]string{
		rule(40),
		"> old echoed message",
		rule(40),
		"  ...later output...",
		rule(40),
		"> real live input",
		rule(40),
		"  [statusline]",
	}, "\n")

	marked, summary := markInputBox(content)

	if summary != "real live input" {
		t.Errorf("summary = %q, want %q", summary, "real live input")
	}
	if !strings.Contains(marked, "> "+inputBoxMarker+" real live input") {
		t.Errorf("real box not tagged:\n%s", marked)
	}
	// The decoy must stay untouched.
	if !strings.Contains(marked, "\n> old echoed message\n") {
		t.Errorf("decoy box was wrongly tagged:\n%s", marked)
	}
	if strings.Count(marked, inputBoxMarkerPrefix) != 1 {
		t.Errorf("expected exactly one marker, got %d:\n%s",
			strings.Count(marked, inputBoxMarkerPrefix), marked)
	}
}

func TestMarkInputBox_OddRuleCount(t *testing.T) {
	// Three rules: a stray divider, then the real box's two rules. Pairing
	// from the bottom must still select the real box.
	content := strings.Join([]string{
		rule(40),
		"  stray divider above",
		rule(40),
		"> typed text",
		rule(40),
	}, "\n")

	marked, summary := markInputBox(content)
	if summary != "typed text" {
		t.Errorf("summary = %q, want %q", summary, "typed text")
	}
	if !strings.Contains(marked, "> "+inputBoxMarker+" typed text") {
		t.Errorf("box not tagged with odd rule count:\n%s", marked)
	}
}

func TestMarkInputBox_MultiLineInput(t *testing.T) {
	// Multi-line input: only the leading prompt line is tagged; continuation
	// lines stay as-is (they are still bracketed by the rules).
	content := strings.Join([]string{
		rule(40),
		"> first line",
		"  second line",
		rule(40),
	}, "\n")

	marked, summary := markInputBox(content)
	if summary != "first line" {
		t.Errorf("summary = %q, want %q", summary, "first line")
	}
	if !strings.Contains(marked, "> "+inputBoxMarker+" first line") {
		t.Errorf("prompt line not tagged:\n%s", marked)
	}
	if !strings.Contains(marked, "\n  second line\n") {
		t.Errorf("continuation line was altered:\n%s", marked)
	}
}

func TestMarkInputBox_EmptyContent(t *testing.T) {
	marked, summary := markInputBox("")
	if marked != "" || summary != "" {
		t.Errorf("empty input: marked=%q summary=%q", marked, summary)
	}
}

func TestMarkInputBox_SingleLineNoNewline(t *testing.T) {
	marked, summary := markInputBox("> lonely line")
	if marked != "> lonely line" {
		t.Errorf("single line changed: %q", marked)
	}
	if summary != "" {
		t.Errorf("summary = %q, want empty", summary)
	}
}

func TestMarkInputBox_Idempotent(t *testing.T) {
	content := strings.Join([]string{
		rule(40),
		"> deploy now",
		rule(40),
	}, "\n")

	once, sum1 := markInputBox(content)
	twice, sum2 := markInputBox(once)

	if once != twice {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
	if sum1 != "deploy now" || sum2 != "deploy now" {
		t.Errorf("summary drift: sum1=%q sum2=%q", sum1, sum2)
	}
	if strings.Count(twice, inputBoxMarkerPrefix) != 1 {
		t.Errorf("double marking: %d markers:\n%s",
			strings.Count(twice, inputBoxMarkerPrefix), twice)
	}
}

func TestMarkInputBox_CRLFBuffer(t *testing.T) {
	// ghostty buffers may be CRLF-terminated; rule detection and rewriting
	// must survive a trailing CR on each line.
	content := strings.Join([]string{
		rule(40) + "\r",
		"> windows input\r",
		rule(40) + "\r",
	}, "\n")

	marked, summary := markInputBox(content)
	if summary != "windows input" {
		t.Errorf("summary = %q, want %q", summary, "windows input")
	}
	if !strings.Contains(marked, "> "+inputBoxMarker+" windows input\r") {
		t.Errorf("CRLF prompt line not tagged correctly:\n%q", marked)
	}
}

func TestMarkInputBox_NBSPPadding(t *testing.T) {
	// Observed in the wild: Claude Code pads the input box with NBSP (U+00A0)
	// and the terminal pads lines with trailing spaces. The body must be
	// extracted clean, and a box holding only padding counts as empty.
	const nbsp = " "

	withText := strings.Join([]string{
		rule(40),
		"> " + nbsp + "Phase 1 進め   ",
		rule(40),
	}, "\n")
	marked, summary := markInputBox(withText)
	if summary != "Phase 1 進め" {
		t.Errorf("summary = %q, want %q", summary, "Phase 1 進め")
	}
	if !strings.Contains(marked, "> "+inputBoxMarker+" Phase 1 進め") {
		t.Errorf("NBSP padding leaked into marked line:\n%q", marked)
	}

	padOnly := strings.Join([]string{
		rule(40),
		"> " + nbsp + "   ",
		rule(40),
	}, "\n")
	marked, summary = markInputBox(padOnly)
	if summary != "(空)" {
		t.Errorf("padding-only box: summary = %q, want %q", summary, "(空)")
	}
	if !strings.Contains(marked, "> "+inputBoxEmptyMarker) {
		t.Errorf("padding-only box not treated as empty:\n%q", marked)
	}
}

func TestMarkInputBox_PreservesTrailingNewline(t *testing.T) {
	content := rule(40) + "\n> x\n" + rule(40) + "\n"
	marked, _ := markInputBox(content)
	if !strings.HasSuffix(marked, "\n") {
		t.Errorf("trailing newline lost: %q", marked)
	}
}
