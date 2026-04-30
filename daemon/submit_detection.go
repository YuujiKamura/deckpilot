package daemon

import (
	"strings"
	"time"
)

type SubmitStatus string

const (
	SubmitOKCleared   SubmitStatus = "ok_cleared"    // 入力行がクリアされた
	SubmitOKThinking  SubmitStatus = "ok_thinking"   // TUI が thinking 状態になった
	SubmitFailedError SubmitStatus = "failed_error"  // TUI にエラー表示
	SubmitUnconfirmed SubmitStatus = "unconfirmed"   // タイムアウト
)

type SubmitResult struct {
	Status    SubmitStatus
	ElapsedMs int
	Evidence  string // 判定根拠の末尾数行(debug用、切り詰めて40行以内)
}

// ExtractInputLine returns the last line starting with "> " in buf.
// Returns "" if no prompt line found.
// TUI's input prompt is typically "> something text here" on its own line.
// For multi-line input, we want the LAST such line (the current prompt).
func ExtractInputLine(buf string) string {
	lines := strings.Split(buf, "\n")
	result := ""
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
			result = trimmed
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
			return strings.TrimRight(line, "\r")
		}
	}
	return ""
}

// ConfirmSubmit observes the pipe's buffer to detect whether a submit happened.
//
// Arguments:
//
//	tailFn       - function to read current buffer tail (abstracted for testability)
//	thinkingStr  - per-agent indicator string (e.g. "Gesticulating" for Claude). Empty = skip.
//	errorStr     - per-agent failure indicator. Empty = skip.
//	preInputLine - the input line content snapshot taken BEFORE sending (e.g. "> atomic番号42")
//	               used to confirm the text moved from prompt into history.
//	timeout      - total observation budget.
//	pollInterval - how often to re-read buffer (e.g. 50ms).
//
// Returns as soon as a definitive signal fires, or at timeout.
// NEVER retries sending. Observation only.
func ConfirmSubmit(
	tailFn func() (string, error),
	thinkingStr, errorStr, preInputLine string,
	timeout, pollInterval time.Duration,
) SubmitResult {
	start := time.Now()
	deadline := start.Add(timeout)

	for time.Now().Before(deadline) {
		buf, err := tailFn()
		if err == nil && buf != "" {
			evidence := tailLines(buf, 40)

			// 1. errorStr: 非空かつ buf に含まれる → SubmitFailedError
			if errorStr != "" && strings.Contains(buf, errorStr) {
				line := findLineContaining(buf, errorStr)
				return SubmitResult{
					Status:    SubmitFailedError,
					ElapsedMs: int(time.Since(start).Milliseconds()),
					Evidence:  line,
				}
			}

			// 2. thinkingStr: 非空かつ buf に含まれる → SubmitOKThinking
			if thinkingStr != "" && strings.Contains(buf, thinkingStr) {
				line := findLineContaining(buf, thinkingStr)
				return SubmitResult{
					Status:    SubmitOKThinking,
					ElapsedMs: int(time.Since(start).Milliseconds()),
					Evidence:  line,
				}
			}

			// 3. 入力行クリア: 現 buf の ExtractInputLine() が空 or "> " or ">"
			//    かつ preInputLine (空でなければ) が buf に含まれる（履歴にスクロールアップ）
			currentInput := ExtractInputLine(buf)
			inputCleared := currentInput == "" || currentInput == "> " || currentInput == ">"
			historyConfirmed := preInputLine == "" || strings.Contains(buf, preInputLine)

			if inputCleared && historyConfirmed {
				return SubmitResult{
					Status:    SubmitOKCleared,
					ElapsedMs: int(time.Since(start).Milliseconds()),
					Evidence:  evidence,
				}
			}
		}

		// まだ確定できない: 次のポーリングまで待つ
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

	// 4. タイムアウト
	buf, _ := tailFn()
	evidence := tailLines(buf, 40)
	return SubmitResult{
		Status:    SubmitUnconfirmed,
		ElapsedMs: int(time.Since(start).Milliseconds()),
		Evidence:  evidence,
	}
}
