// Package cmd: ghostmark — tags the live Claude Code input box in a terminal
// buffer so a reader (human or AI) does not mistake unsubmitted input-box text
// for an already-sent instruction.
//
// Background: `deckpilot show` prints the ghostty terminal buffer as plain
// text. ghostty's TAIL command flattens the grid via plainString, dropping all
// color/dim attributes — so the dim "autosuggest ghost" text that Claude Code
// shows inside its input box is indistinguishable from real typed input once
// it reaches deckpilot. That ambiguity caused an operator to read a ghost
// "push して" suggestion as a real instruction.
//
// We cannot recover the dim attribute (it is gone before deckpilot sees the
// buffer) nor the cursor column (ghostty's CURSOR_POS returns CURSOR_UNAVAILABLE
// because the snapshot provider never fills it). What we can do with the plain
// text alone is locate the input-box *line* and tag it, so its content is never
// read as a submitted instruction.
package cmd

import (
	"strings"
	"unicode"
)

// inputBoxMarker is the tag inserted into the live input-box prompt line.
// inputBoxEmptyMarker is used when the box has no text.
// Readers (and the idempotency check) match on inputBoxMarkerPrefix.
const (
	inputBoxMarkerPrefix = "[入力欄/未送信"
	inputBoxMarker       = "[入力欄/未送信]"
	inputBoxEmptyMarker  = "[入力欄/未送信・空]"
)

// ruleRune is U+2500 BOX DRAWINGS LIGHT HORIZONTAL — the character Claude Code
// uses to draw the horizontal rules above and below its input box.
const ruleRune = '─'

// minRuleLen is the minimum run of ruleRune for a line to count as a box rule.
// Short `───` dividers inside tool output stay below this threshold; the real
// input-box rules span the full terminal width (~80+).
const minRuleLen = 20

// isRuleLine reports whether line is one of Claude Code's input-box rule lines:
// a run of at least minRuleLen ruleRune characters, ignoring surrounding
// whitespace and a trailing CR (ghostty buffers may be CRLF-terminated).
func isRuleLine(line string) bool {
	s := strings.TrimRight(line, "\r")
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	n := 0
	for _, r := range s {
		if r != ruleRune {
			return false
		}
		n++
	}
	return n >= minRuleLen
}

// isPromptLine reports whether line is the input-box prompt line: a ">" after
// optional leading whitespace.
func isPromptLine(line string) bool {
	s := strings.TrimLeft(strings.TrimRight(line, "\r"), " \t")
	return strings.HasPrefix(s, ">")
}

// markInputBox locates Claude Code's live input box in a terminal buffer and
// rewrites its prompt line so a reader can tell unsubmitted input-box content
// apart from sent instructions.
//
// The input box is the bottom-most pair of rule lines whose first enclosed line
// is a prompt line. markInputBox returns the rewritten buffer and a summary of
// what was tagged: the prompt body, "(空)" for an empty box, or "" when no
// input box was found. The summary is meant to be echoed on a trusted side
// channel (stderr) so the reader can cross-check the in-band marker.
//
// Marking is idempotent: a prompt line that already carries the marker is left
// untouched.
func markInputBox(content string) (marked string, summary string) {
	if content == "" {
		return content, ""
	}

	lines := strings.Split(content, "\n")

	// Collect rule-line indices.
	var rules []int
	for i, ln := range lines {
		if isRuleLine(ln) {
			rules = append(rules, i)
		}
	}

	// Examine adjacent rule pairs from the bottom; the first pair whose first
	// enclosed line is a prompt line is the live input box.
	for k := len(rules) - 1; k >= 1; k-- {
		r1, r2 := rules[k-1], rules[k]
		if r2-r1 < 2 {
			continue // no body between the rules
		}
		if !isPromptLine(lines[r1+1]) {
			continue
		}
		s := markPromptLine(lines[r1+1])
		lines[r1+1] = s.line
		return strings.Join(lines, "\n"), s.summary
	}

	return content, ""
}

// promptMark is the result of rewriting a single prompt line.
type promptMark struct {
	line    string // the rewritten prompt line
	summary string // the prompt body (for the stderr cross-check)
}

// markPromptLine rewrites one input-box prompt line, inserting the marker.
// An already-marked line is returned unchanged (idempotency).
func markPromptLine(line string) promptMark {
	cr := ""
	body := line
	if strings.HasSuffix(body, "\r") {
		cr = "\r"
		body = strings.TrimSuffix(body, "\r")
	}

	indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
	afterIndent := strings.TrimLeft(body, " \t")

	// afterIndent starts with ">" (isPromptLine guaranteed it). Trim the
	// surrounding whitespace with a Unicode-aware predicate: Claude Code pads
	// the input box with NBSP (U+00A0) and the terminal pads lines with
	// trailing spaces, so an ASCII-only trim would leak padding into the body
	// and misclassify an empty box as non-empty.
	rest := strings.TrimPrefix(afterIndent, ">")
	text := strings.TrimFunc(rest, unicode.IsSpace)

	// Idempotency: leave an already-marked line alone, but recover the
	// original body so the summary stays stable across repeated calls.
	if strings.Contains(text, inputBoxMarkerPrefix) {
		return promptMark{line: line, summary: recoverMarkedBody(text)}
	}

	if text == "" {
		return promptMark{
			line:    indent + "> " + inputBoxEmptyMarker + cr,
			summary: "(空)",
		}
	}
	return promptMark{
		line:    indent + "> " + inputBoxMarker + " " + text + cr,
		summary: text,
	}
}

// recoverMarkedBody extracts the original input-box body from already-tagged
// prompt-line text, so the stderr summary stays stable if markInputBox runs
// twice over the same buffer. Text it does not recognise is returned as-is.
func recoverMarkedBody(text string) string {
	if strings.HasPrefix(text, inputBoxEmptyMarker) {
		return "(空)"
	}
	if rest, ok := strings.CutPrefix(text, inputBoxMarker); ok {
		return strings.TrimSpace(rest)
	}
	return text
}
