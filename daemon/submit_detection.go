package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

type SubmitStatus string

const (
	// SubmitOKCleared: the buffer moved beyond the pre-Enter snapshot —
	// something happened after Enter was sent (prompt cleared, response
	// started, etc.). The submit was accepted by the TUI.
	SubmitOKCleared SubmitStatus = "ok_cleared"

	// SubmitFailedStuck: three consecutive buffer samples all hash-equal to
	// the pre-Enter snapshot. The TUI never redrew after Enter — the submit
	// never fired. Positive failure, not a timeout.
	SubmitFailedStuck SubmitStatus = "failed_stuck"

	// SubmitUnconfirmed: observation window expired without seeing either
	// movement beyond pre-Enter or three stable confirms. Most commonly
	// this happens when tailFn errors or pollInterval is too large to fit
	// the required number of samples in the window.
	SubmitUnconfirmed SubmitStatus = "unconfirmed"
)

type SubmitResult struct {
	Status    SubmitStatus
	ElapsedMs int
	Evidence  string
}

// stableRepeatThreshold is the number of consecutive buffer samples that must
// all hash-equal the pre-Enter reference before we declare the submit stuck.
// Three is the minimum that distinguishes "stuck at typed input" from "still
// in the brief window between Enter keystroke arrival and TUI redraw". See
// design discussion: 2 samples can only observe "something changed"; the 3rd
// sample is what proves "and then stayed changed" vs "changed and stayed".
const stableRepeatThreshold = 3

// promptChars are characters that commonly lead a prompt line. Retained
// because ExtractInputLine is still used by other daemon code (watcher /
// legacy diagnostics) even though submit detection no longer regex-matches
// any per-agent markers.
var promptChars = []string{"❯", ">", "$", "%", "#"}

// ExtractInputLine returns the last line whose first non-space rune is a
// common prompt character. Returns "" if no such line is found.
func ExtractInputLine(buf string) string {
	result := ""
	for _, line := range strings.Split(buf, "\n") {
		trimmed := strings.TrimRight(line, " \r\t")
		lt := strings.TrimLeft(trimmed, " \t")
		if lt == "" {
			continue
		}
		for _, pc := range promptChars {
			if strings.HasPrefix(lt, pc) {
				result = trimmed
				break
			}
		}
	}
	return result
}

// BufHash returns a stable SHA-256 hex digest for a buffer snapshot. Two
// buffers with the same hash are byte-identical; two with different hashes
// differ somewhere. Exported so ipc.go's send path can capture the pre-Enter
// reference hash and pass it into ConfirmSubmit.
func BufHash(buf string) string {
	h := sha256.Sum256([]byte(buf))
	return hex.EncodeToString(h[:])
}

// tailLines returns the last n lines of buf as a single string.
func tailLines(buf string, n int) string {
	lines := strings.Split(buf, "\n")
	if len(lines) <= n {
		return buf
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// ConfirmSubmit observes the TUI buffer to decide whether pressing Enter
// actually submitted. The algorithm is pure buffer-hash comparison — no
// per-agent regex, no string-matching against the sent text. This keeps
// the logic robust to TUI transformations like Gemini's replacement of
// "@path/to/file.jpg" with "[Image file.jpg]" in scrollback, which would
// break any probe-based approach.
//
// Protocol:
//
//  1. Caller captures the buffer-hash IMMEDIATELY AFTER typing lands but
//     BEFORE pressing Enter → `preEnterHash`. This is the "idle with text
//     in prompt" state.
//  2. Caller presses Enter.
//  3. Caller invokes ConfirmSubmit, which polls the buffer:
//     - If any sample's hash != preEnterHash → SubmitOKCleared (buffer moved
//       beyond the pre-Enter state, so Enter did something).
//     - If stableRepeatThreshold consecutive samples all hash-equal
//       preEnterHash → SubmitFailedStuck (TUI never redrew; submit never
//       fired).
//     - If neither condition is reached before timeout → SubmitUnconfirmed.
//
// Minimum 3 samples for stuck detection, as discussed in the design note:
// 2 samples can only confirm equality; the 3rd is needed to rule out the
// transient overlap between Enter arriving and the TUI beginning to redraw.
func ConfirmSubmit(
	tailFn func() (string, error),
	preEnterHash string,
	timeout, pollInterval time.Duration,
) SubmitResult {
	start := time.Now()
	deadline := start.Add(timeout)

	stableRepeats := 0

	for time.Now().Before(deadline) {
		buf, err := tailFn()
		if err == nil && buf != "" {
			h := BufHash(buf)
			if h != preEnterHash {
				return SubmitResult{
					Status:    SubmitOKCleared,
					ElapsedMs: int(time.Since(start).Milliseconds()),
					Evidence:  tailLines(buf, 40),
				}
			}
			stableRepeats++
			if stableRepeats >= stableRepeatThreshold {
				return SubmitResult{
					Status:    SubmitFailedStuck,
					ElapsedMs: int(time.Since(start).Milliseconds()),
					Evidence:  tailLines(buf, 40),
				}
			}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if pollInterval > remaining {
			time.Sleep(remaining)
		} else {
			time.Sleep(pollInterval)
		}
	}

	buf, _ := tailFn()
	return SubmitResult{
		Status:    SubmitUnconfirmed,
		ElapsedMs: int(time.Since(start).Milliseconds()),
		Evidence:  tailLines(buf, 40),
	}
}
