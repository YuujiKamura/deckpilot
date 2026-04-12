package daemon

import (
	"strings"
	"time"
)

type SubmitStatus string

const (
	SubmitOKCleared   SubmitStatus = "ok_cleared"   // text moved out of input area
	SubmitOKThinking  SubmitStatus = "ok_thinking"  // TUI shows thinking indicator
	SubmitFailedError SubmitStatus = "failed_error" // TUI shows error string
	SubmitUnconfirmed SubmitStatus = "unconfirmed"  // polling timeout
)

type SubmitResult struct {
	Status    SubmitStatus
	ElapsedMs int
	Evidence  string
}

// inputAreaTailLines is the number of trailing buffer lines considered part of
// the TUI's input/status area. Most TUIs keep the prompt + a couple of status
// rows at the bottom (claude-code has prompt + ~3 status lines; cmd/bash use 1).
const inputAreaTailLines = 6

// promptChars are characters that commonly lead a prompt line.
// Checked at the start of a stripped line (after leading whitespace).
var promptChars = []string{"❯", ">", "$", "%", "#"}

// ExtractInputLine returns the last line whose first non-space rune is a
// common prompt character. Returns "" if no such line is found. The returned
// string is right-trimmed of trailing whitespace/CR.
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

// tailLines returns the last n lines of buf as a single string.
func tailLines(buf string, n int) string {
	lines := strings.Split(buf, "\n")
	if len(lines) <= n {
		return buf
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// findLineContaining returns the first line in buf that contains substr, or "".
func findLineContaining(buf, substr string) string {
	for _, line := range strings.Split(buf, "\n") {
		if strings.Contains(line, substr) {
			return strings.TrimRight(line, " \r\t")
		}
	}
	return ""
}

// containsInTail reports whether substr appears in the last tailN lines of buf.
func containsInTail(buf, substr string, tailN int) bool {
	if substr == "" {
		return false
	}
	lines := strings.Split(buf, "\n")
	start := len(lines) - tailN
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// ConfirmSubmit observes the pipe's buffer to detect whether a submit happened.
//
// Arguments:
//
//	tailFn        - reads current buffer tail (abstracted for testability).
//	thinkingStr   - per-agent indicator string (e.g. "Gesticulating"). Empty = skip.
//	errorStr      - per-agent failure indicator. Empty = skip.
//	preSubmitText - the actual text we sent. Used to detect submit by checking
//	                whether the text has moved out of the input area (last few
//	                buffer lines) into scrollback history. Empty = skip this
//	                signal (detection relies on thinkingStr only).
//	timeout       - total observation budget.
//	pollInterval  - how often to re-read buffer.
//
// Priority: errorStr > thinkingStr > text-moved-out-of-input > timeout.
// Never retries sending. Observation only.
func ConfirmSubmit(
	tailFn func() (string, error),
	thinkingStr, errorStr, preSubmitText string,
	timeout, pollInterval time.Duration,
) SubmitResult {
	start := time.Now()
	deadline := start.Add(timeout)

	for time.Now().Before(deadline) {
		buf, err := tailFn()
		if err == nil && buf != "" {
			evidence := tailLines(buf, 40)

			if errorStr != "" && strings.Contains(buf, errorStr) {
				return SubmitResult{
					Status:    SubmitFailedError,
					ElapsedMs: int(time.Since(start).Milliseconds()),
					Evidence:  findLineContaining(buf, errorStr),
				}
			}

			if thinkingStr != "" && strings.Contains(buf, thinkingStr) {
				return SubmitResult{
					Status:    SubmitOKThinking,
					ElapsedMs: int(time.Since(start).Milliseconds()),
					Evidence:  findLineContaining(buf, thinkingStr),
				}
			}

			// Text-moved-out: the text we typed must appear somewhere in buffer
			// (confirming Phase 1 delivery), but NOT in the bottom input area
			// (confirming submit caused it to scroll up into history).
			if preSubmitText != "" {
				inBuf := strings.Contains(buf, preSubmitText)
				inInputArea := containsInTail(buf, preSubmitText, inputAreaTailLines)
				if inBuf && !inInputArea {
					return SubmitResult{
						Status:    SubmitOKCleared,
						ElapsedMs: int(time.Since(start).Milliseconds()),
						Evidence:  evidence,
					}
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
