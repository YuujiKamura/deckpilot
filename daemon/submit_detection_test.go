package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- ExtractInputLine tests ---

func TestExtractInputLine_LastPromptLine(t *testing.T) {
	buf := "some output\n> first\nmore output\n> last line here"
	got := ExtractInputLine(buf)
	if got != "> last line here" {
		t.Errorf("expected '> last line here', got %q", got)
	}
}

func TestExtractInputLine_NoPrompt(t *testing.T) {
	buf := "just some output\nno prompt here"
	got := ExtractInputLine(buf)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractInputLine_EmptyBuf(t *testing.T) {
	if got := ExtractInputLine(""); got != "" {
		t.Errorf("expected empty string for empty buf, got %q", got)
	}
}

func TestExtractInputLine_BarePrompt(t *testing.T) {
	buf := "output\n>"
	if got := ExtractInputLine(buf); got != ">" {
		t.Errorf("expected '>', got %q", got)
	}
}

func TestExtractInputLine_PromptWithSpace(t *testing.T) {
	// Trailing whitespace is stripped by ExtractInputLine; "> " becomes ">".
	buf := "output\n> "
	if got := ExtractInputLine(buf); got != ">" {
		t.Errorf("expected '>' (trailing space trimmed), got %q", got)
	}
}

func TestExtractInputLine_MultiplePrompts(t *testing.T) {
	buf := "> first input\nresponse line\n> second input\nanother response\n> third input"
	if got := ExtractInputLine(buf); got != "> third input" {
		t.Errorf("expected '> third input', got %q", got)
	}
}

func TestExtractInputLine_ClaudePrompt(t *testing.T) {
	// Claude Code uses "❯" not ">". Must match.
	buf := "output\n❯ some typed text"
	if got := ExtractInputLine(buf); got != "❯ some typed text" {
		t.Errorf("expected claude prompt matched, got %q", got)
	}
}

func TestExtractInputLine_ShellPrompts(t *testing.T) {
	// bash/zsh/fish style
	for _, pc := range []string{"$", "%", "#"} {
		buf := "out\n" + pc + " command"
		want := pc + " command"
		if got := ExtractInputLine(buf); got != want {
			t.Errorf("prompt %q: expected %q, got %q", pc, want, got)
		}
	}
}

// --- ConfirmSubmit tests ---

func fixedTailFn(buf string) func() (string, error) {
	return func() (string, error) { return buf, nil }
}

func stepTailFn(steps []string) func() (string, error) {
	count := 0
	return func() (string, error) {
		if count >= len(steps) {
			return steps[len(steps)-1], nil
		}
		s := steps[count]
		count++
		return s, nil
	}
}

func TestConfirmSubmit_OKThinking(t *testing.T) {
	buf := "previous output\nGesticulating...\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating", "",
		"hello",
		200*time.Millisecond, 10*time.Millisecond,
	)
	if result.Status != SubmitOKThinking {
		t.Errorf("expected SubmitOKThinking, got %q (evidence: %s)", result.Status, result.Evidence)
	}
	if result.Evidence == "" {
		t.Error("evidence should not be empty for OKThinking")
	}
}

func TestConfirmSubmit_FailedError(t *testing.T) {
	buf := "some output\nAPI Error: rate limit\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating", "API Error",
		"my query",
		200*time.Millisecond, 10*time.Millisecond,
	)
	if result.Status != SubmitFailedError {
		t.Errorf("expected SubmitFailedError, got %q", result.Status)
	}
}

func TestConfirmSubmit_OKCleared_TextMovedOut(t *testing.T) {
	// Text appears in buffer but NOT in last 6 lines (input area).
	// Many blank lines push the history up so the text is outside the tail window.
	text := "atomic番号42"
	history := "history: " + text + "\nline2\nline3\nline4\nline5\nline6\nline7\n> "
	result := ConfirmSubmit(
		fixedTailFn(history),
		"", "",
		text,
		200*time.Millisecond, 10*time.Millisecond,
	)
	if result.Status != SubmitOKCleared {
		t.Errorf("expected SubmitOKCleared, got %q (evidence: %s)", result.Status, result.Evidence)
	}
}

func TestConfirmSubmit_NotCleared_TextStillInInputArea(t *testing.T) {
	// Text still in the last line (unsubmitted).
	text := "still typing"
	buf := "history\n❯ " + text
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"", "",
		text,
		100*time.Millisecond, 20*time.Millisecond,
	)
	if result.Status != SubmitUnconfirmed {
		t.Errorf("expected SubmitUnconfirmed (text still in input area), got %q", result.Status)
	}
}

func TestConfirmSubmit_Unconfirmed_NoSignal(t *testing.T) {
	buf := "some output\n❯ still typing"
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating", "API Error",
		"still typing",
		100*time.Millisecond, 20*time.Millisecond,
	)
	if result.Status != SubmitUnconfirmed {
		t.Errorf("expected SubmitUnconfirmed, got %q", result.Status)
	}
	if result.ElapsedMs < 90 {
		t.Errorf("expected elapsed ~100ms, got %dms", result.ElapsedMs)
	}
}

func TestConfirmSubmit_ErrorBeatsThinking(t *testing.T) {
	buf := "Gesticulating...\nAPI Error: something\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating", "API Error",
		"query",
		200*time.Millisecond, 10*time.Millisecond,
	)
	if result.Status != SubmitFailedError {
		t.Errorf("expected SubmitFailedError, got %q", result.Status)
	}
}

func TestConfirmSubmit_StepTransition_TextMovesOut(t *testing.T) {
	// Initially text is in last line (not submitted).
	// Then after a delay it moves into history and input becomes empty.
	text := "hello world"
	padding := strings.Repeat("line\n", 6)
	steps := []string{
		"output\n❯ " + text,                   // typing, not submitted
		"output\n❯ " + text,                   // still typing
		"history: " + text + "\n" + padding + "❯ ", // submitted, text scrolled up
	}
	result := ConfirmSubmit(
		stepTailFn(steps),
		"", "",
		text,
		500*time.Millisecond, 10*time.Millisecond,
	)
	if result.Status != SubmitOKCleared {
		t.Errorf("expected SubmitOKCleared after transition, got %q (evidence: %s)", result.Status, result.Evidence)
	}
}

func TestConfirmSubmit_TailFnError(t *testing.T) {
	errorFn := func() (string, error) { return "", fmt.Errorf("pipe closed") }
	result := ConfirmSubmit(
		errorFn,
		"Thinking", "Error",
		"query",
		80*time.Millisecond, 20*time.Millisecond,
	)
	if result.Status != SubmitUnconfirmed {
		t.Errorf("expected SubmitUnconfirmed on tailFn error, got %q", result.Status)
	}
}

func TestConfirmSubmit_EmptyPreSubmitText_NoClearedDetection(t *testing.T) {
	// With empty preSubmitText we cannot use the cleared signal.
	// Must fall through to Unconfirmed unless thinking/error fires.
	buf := "some output\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"", "",
		"", // empty preSubmitText
		80*time.Millisecond, 10*time.Millisecond,
	)
	if result.Status != SubmitUnconfirmed {
		t.Errorf("expected SubmitUnconfirmed with empty preSubmitText, got %q", result.Status)
	}
}

func TestConfirmSubmit_EmptyPreSubmitText_ThinkingStillWorks(t *testing.T) {
	// Even with empty preSubmitText, thinkingStr should fire OKThinking.
	buf := "Gesticulating...\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating", "",
		"",
		80*time.Millisecond, 10*time.Millisecond,
	)
	if result.Status != SubmitOKThinking {
		t.Errorf("expected SubmitOKThinking with empty preSubmitText, got %q", result.Status)
	}
}
