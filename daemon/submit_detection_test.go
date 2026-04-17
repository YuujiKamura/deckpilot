package daemon

import (
	"fmt"
	"testing"
	"time"
)

// --- ExtractInputLine tests ---

func TestExtractInputLine_LastPromptLine(t *testing.T) {
	buf := "some output\n> first\nmore output\n> last line here"
	if got := ExtractInputLine(buf); got != "> last line here" {
		t.Errorf("expected '> last line here', got %q", got)
	}
}

func TestExtractInputLine_NoPrompt(t *testing.T) {
	if got := ExtractInputLine("just some output\nno prompt here"); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractInputLine_EmptyBuf(t *testing.T) {
	if got := ExtractInputLine(""); got != "" {
		t.Errorf("expected empty string for empty buf, got %q", got)
	}
}

func TestExtractInputLine_ClaudePrompt(t *testing.T) {
	if got := ExtractInputLine("output\n❯ some typed text"); got != "❯ some typed text" {
		t.Errorf("expected claude prompt matched, got %q", got)
	}
}

// --- BufHash tests ---

func TestBufHash_StableForIdenticalInput(t *testing.T) {
	a := BufHash("hello world\n> ")
	b := BufHash("hello world\n> ")
	if a != b {
		t.Errorf("same input must hash equal; got %q vs %q", a, b)
	}
}

func TestBufHash_DifferentForSingleByteDiff(t *testing.T) {
	a := BufHash("hello world\n> ")
	b := BufHash("hello world\n❯ ")
	if a == b {
		t.Errorf("single-glyph change must hash differently; both %q", a)
	}
}

// --- ConfirmSubmit tests ---

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

// countingTailFn always returns buf; reports how many times it was called.
func countingTailFn(buf string) (func() (string, error), *int) {
	calls := 0
	return func() (string, error) {
		calls++
		return buf, nil
	}, &calls
}

// First sample already differs from pre-Enter hash → OKCleared on first call.
// This is the common success path: by the time we poll, the TUI has already
// cleared the prompt and started rendering.
func TestConfirmSubmit_OKCleared_FirstSampleDiffers(t *testing.T) {
	preEnter := BufHash("text in prompt\n❯ sent text")
	postEnter := "response line\n❯ " // different buffer
	tailFn, calls := countingTailFn(postEnter)

	result := ConfirmSubmit(tailFn, preEnter, 500*time.Millisecond, 20*time.Millisecond)

	if result.Status != SubmitOKCleared {
		t.Fatalf("expected SubmitOKCleared, got %q", result.Status)
	}
	if *calls != 1 {
		t.Errorf("OKCleared should fire on first divergent sample; got %d calls", *calls)
	}
}

// Buffer identical to preEnterHash on every poll → after 3 matching samples
// we declare FailedStuck. Minimum 3-sample rule.
func TestConfirmSubmit_FailedStuck_RequiresExactlyThreeSamples(t *testing.T) {
	buf := "history\n❯ stuck text"
	preEnter := BufHash(buf)
	tailFn, calls := countingTailFn(buf)

	result := ConfirmSubmit(tailFn, preEnter, 500*time.Millisecond, 5*time.Millisecond)

	if result.Status != SubmitFailedStuck {
		t.Fatalf("expected SubmitFailedStuck, got %q", result.Status)
	}
	if *calls != 3 {
		t.Errorf("expected exactly 3 tailFn calls for stuck detection, got %d", *calls)
	}
}

// 2 samples at preEnterHash then a 3rd that differs → OKCleared.
// Even after 2 matching samples, a subsequent change wins.
func TestConfirmSubmit_OKCleared_ChangeOnThirdSample(t *testing.T) {
	stuckBuf := "history\n❯ typed text"
	preEnter := BufHash(stuckBuf)
	steps := []string{
		stuckBuf,
		stuckBuf,
		"response line\n❯ ", // finally moved
	}
	tailFn := stepTailFn(steps)

	result := ConfirmSubmit(tailFn, preEnter, 500*time.Millisecond, 5*time.Millisecond)

	if result.Status != SubmitOKCleared {
		t.Fatalf("expected SubmitOKCleared on 3rd-sample change, got %q", result.Status)
	}
}

// Only 2 matching samples fit in the window → must return Unconfirmed
// (cannot reach 3-sample threshold, cannot prove stuck).
func TestConfirmSubmit_FailedStuck_TwoSamplesNotEnough(t *testing.T) {
	buf := "history\n❯ stuck text"
	preEnter := BufHash(buf)

	calls := 0
	tailFn := func() (string, error) {
		calls++
		if calls > 2 {
			return "", fmt.Errorf("simulated third-read failure")
		}
		return buf, nil
	}

	result := ConfirmSubmit(tailFn, preEnter, 80*time.Millisecond, 10*time.Millisecond)

	if result.Status == SubmitFailedStuck {
		t.Fatalf("FailedStuck must NOT fire with only 2 valid samples (got %d calls, status %q)",
			calls, result.Status)
	}
	if result.Status != SubmitUnconfirmed {
		t.Fatalf("expected SubmitUnconfirmed, got %q", result.Status)
	}
}

// Stuck detection is sample-count driven, not wall-time driven. Simulate
// variable IPC jitter per tailFn call and verify the algorithm still decides
// correctly after exactly 3 samples.
func TestConfirmSubmit_FailedStuck_UnderIPCJitter(t *testing.T) {
	buf := "history\n❯ stuck text"
	preEnter := BufHash(buf)

	calls := 0
	jitter := []time.Duration{
		30 * time.Millisecond,
		250 * time.Millisecond,
		80 * time.Millisecond,
	}
	tailFn := func() (string, error) {
		d := jitter[calls%len(jitter)]
		calls++
		time.Sleep(d)
		return buf, nil
	}

	result := ConfirmSubmit(tailFn, preEnter, 5*time.Second, 10*time.Millisecond)

	if result.Status != SubmitFailedStuck {
		t.Fatalf("expected SubmitFailedStuck under IPC jitter, got %q", result.Status)
	}
	if calls != 3 {
		t.Errorf("sample-count invariant broken under jitter: expected 3 calls, got %d", calls)
	}
}

// Tail latency >> pollInterval: stuck detection still fires in exactly
// 3 samples. pollInterval does not need to be small relative to IPC cost.
func TestConfirmSubmit_FailedStuck_PollIntervalSmallerThanTailLatency(t *testing.T) {
	buf := "history\n❯ stuck text"
	preEnter := BufHash(buf)

	calls := 0
	tailFn := func() (string, error) {
		calls++
		time.Sleep(150 * time.Millisecond)
		return buf, nil
	}

	result := ConfirmSubmit(tailFn, preEnter, 2*time.Second, 10*time.Millisecond)

	if result.Status != SubmitFailedStuck {
		t.Fatalf("expected SubmitFailedStuck, got %q", result.Status)
	}
	if calls != 3 {
		t.Errorf("expected exactly 3 calls, got %d", calls)
	}
}

// Buffer changes on every poll and never matches preEnterHash → every sample
// is divergent, so OKCleared fires on the FIRST sample.
func TestConfirmSubmit_OKCleared_ContinuouslyChanging(t *testing.T) {
	preEnter := BufHash("initial state")
	calls := 0
	tailFn := func() (string, error) {
		calls++
		return fmt.Sprintf("response chunk %d\n❯ ", calls), nil
	}

	result := ConfirmSubmit(tailFn, preEnter, 80*time.Millisecond, 10*time.Millisecond)

	if result.Status != SubmitOKCleared {
		t.Fatalf("expected SubmitOKCleared (buffer differs from preEnter), got %q", result.Status)
	}
	if calls != 1 {
		t.Errorf("expected first-sample decision, got %d calls", calls)
	}
}

// tailFn errors every time → no observation possible → Unconfirmed on timeout.
func TestConfirmSubmit_TailFnError(t *testing.T) {
	errorFn := func() (string, error) { return "", fmt.Errorf("pipe closed") }

	result := ConfirmSubmit(errorFn, "anyhash", 80*time.Millisecond, 20*time.Millisecond)

	if result.Status != SubmitUnconfirmed {
		t.Errorf("expected SubmitUnconfirmed on tailFn error, got %q", result.Status)
	}
}

// The Gemini @path transform scenario: the pre-Enter buffer shows the raw
// "@C:/path/to/file.jpg" text in the prompt; after Enter, Gemini replaces
// that with "[Image file.jpg]" in scrollback. A probe-based implementation
// would fail to find the exact sent text and mis-report Unconfirmed. This
// hash-based impl sees the buffers differ and returns OKCleared.
func TestConfirmSubmit_OKCleared_GeminiAtPathTransform(t *testing.T) {
	preEnterBuf := "> analyze: @C:/tmp/R0010850.JPG"
	preEnter := BufHash(preEnterBuf)
	// After Enter: Gemini transformed @path to [Image ...] and started response.
	postEnterBuf := "> analyze: [Image R0010850.JPG]\n✦ {result...}\n> "

	tailFn, _ := countingTailFn(postEnterBuf)
	result := ConfirmSubmit(tailFn, preEnter, 500*time.Millisecond, 20*time.Millisecond)

	if result.Status != SubmitOKCleared {
		t.Fatalf("expected SubmitOKCleared despite @path→[Image] transform, got %q", result.Status)
	}
}
