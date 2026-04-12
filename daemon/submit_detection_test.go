package daemon

import (
	"fmt"
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
	got := ExtractInputLine("")
	if got != "" {
		t.Errorf("expected empty string for empty buf, got %q", got)
	}
}

func TestExtractInputLine_BarePrompt(t *testing.T) {
	buf := "output\n>"
	got := ExtractInputLine(buf)
	if got != ">" {
		t.Errorf("expected '>', got %q", got)
	}
}

func TestExtractInputLine_PromptWithSpace(t *testing.T) {
	buf := "output\n> "
	got := ExtractInputLine(buf)
	if got != "> " {
		t.Errorf("expected '> ', got %q", got)
	}
}

func TestExtractInputLine_MultiplePrompts(t *testing.T) {
	buf := "> first input\nresponse line\n> second input\nanother response\n> third input"
	got := ExtractInputLine(buf)
	if got != "> third input" {
		t.Errorf("expected '> third input', got %q", got)
	}
}

// --- ConfirmSubmit tests ---

// fixed tailFn: 常に同じバッファを返す
func fixedTailFn(buf string) func() (string, error) {
	return func() (string, error) {
		return buf, nil
	}
}

// step tailFn: 呼び出し回数に応じてバッファを切り替える
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
		"Gesticulating", // thinkingStr
		"",              // errorStr
		"> hello",       // preInputLine
		200*time.Millisecond,
		10*time.Millisecond,
	)
	if result.Status != SubmitOKThinking {
		t.Errorf("expected SubmitOKThinking, got %q (evidence: %s)", result.Status, result.Evidence)
	}
	if result.Evidence == "" {
		t.Error("evidence should not be empty for OKThinking")
	}
}

func TestConfirmSubmit_FailedError(t *testing.T) {
	buf := "some output\nAPI Error: rate limit exceeded\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating",    // thinkingStr
		"API Error",        // errorStr
		"> my query",       // preInputLine
		200*time.Millisecond,
		10*time.Millisecond,
	)
	if result.Status != SubmitFailedError {
		t.Errorf("expected SubmitFailedError, got %q (evidence: %s)", result.Status, result.Evidence)
	}
	if result.Evidence == "" {
		t.Error("evidence should not be empty for FailedError")
	}
}

func TestConfirmSubmit_OKCleared(t *testing.T) {
	// preInputLine が履歴に現れ、現在の入力行がクリアされている状態
	preInput := "> atomic番号42"
	buf := fmt.Sprintf("history: %s\nsome response here\n> ", preInput)
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"",        // thinkingStr (skip)
		"",        // errorStr (skip)
		preInput,  // preInputLine
		200*time.Millisecond,
		10*time.Millisecond,
	)
	if result.Status != SubmitOKCleared {
		t.Errorf("expected SubmitOKCleared, got %q (evidence: %s)", result.Status, result.Evidence)
	}
}

func TestConfirmSubmit_Unconfirmed(t *testing.T) {
	// 何も変わらない: 入力行がそのまま残り、thinkingもerrorもない
	preInput := "> still typing"
	buf := fmt.Sprintf("some output\n%s", preInput)
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating", // thinkingStr: buf に含まれない
		"API Error",     // errorStr: buf に含まれない
		preInput,        // preInputLine: buf に現在の入力行として存在
		100*time.Millisecond,
		20*time.Millisecond,
	)
	if result.Status != SubmitUnconfirmed {
		t.Errorf("expected SubmitUnconfirmed, got %q", result.Status)
	}
	if result.ElapsedMs < 90 {
		t.Errorf("expected elapsed ~100ms, got %dms", result.ElapsedMs)
	}
}

func TestConfirmSubmit_ErrorTakesPriorityOverThinking(t *testing.T) {
	// error と thinking が両方 buf に含まれる場合、error が優先
	buf := "Gesticulating...\nAPI Error: something went wrong\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"Gesticulating",
		"API Error",
		"> query",
		200*time.Millisecond,
		10*time.Millisecond,
	)
	if result.Status != SubmitFailedError {
		t.Errorf("expected SubmitFailedError (error priority), got %q", result.Status)
	}
}

func TestConfirmSubmit_StepTransition_ClearedAfterDelay(t *testing.T) {
	// 最初は入力行あり → 数回後に履歴に移行してクリアされる
	preInput := "> hello world"
	steps := []string{
		fmt.Sprintf("output\n%s", preInput),          // まだ入力中
		fmt.Sprintf("output\n%s", preInput),          // まだ入力中
		fmt.Sprintf("output\nhistory: %s\n> ", preInput), // クリア済み
	}
	result := ConfirmSubmit(
		stepTailFn(steps),
		"",
		"",
		preInput,
		500*time.Millisecond,
		10*time.Millisecond,
	)
	if result.Status != SubmitOKCleared {
		t.Errorf("expected SubmitOKCleared after step transition, got %q", result.Status)
	}
}

func TestConfirmSubmit_TailFnError(t *testing.T) {
	// tailFn がエラーを返す場合はスキップしてタイムアウトに至る
	errorFn := func() (string, error) {
		return "", fmt.Errorf("pipe closed")
	}
	result := ConfirmSubmit(
		errorFn,
		"Thinking",
		"Error",
		"> query",
		80*time.Millisecond,
		20*time.Millisecond,
	)
	if result.Status != SubmitUnconfirmed {
		t.Errorf("expected SubmitUnconfirmed on tailFn error, got %q", result.Status)
	}
}

func TestConfirmSubmit_EmptyPreInputLine(t *testing.T) {
	// preInputLine が空の場合、履歴確認をスキップして入力クリアだけで判定
	buf := "some output\n> "
	result := ConfirmSubmit(
		fixedTailFn(buf),
		"",
		"",
		"", // preInputLine 空
		200*time.Millisecond,
		10*time.Millisecond,
	)
	if result.Status != SubmitOKCleared {
		t.Errorf("expected SubmitOKCleared with empty preInputLine, got %q", result.Status)
	}
}
