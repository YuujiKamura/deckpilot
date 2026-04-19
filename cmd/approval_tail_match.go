package cmd

import "strings"

// matchTail scans the last `tailLines` lines of content for any of the
// given patterns. Extracted from DetectApprovalPromptForAgent so
// adapters share one matcher. Kept deliberately simple: Contains check,
// first-match wins. Agents with tricky patterns (regex, multi-line)
// should implement Detect directly instead of using this helper.
func matchTail(content string, patterns []string, tailLines int) (matched string, found bool) {
	if tailLines <= 0 {
		tailLines = 15
	}
	promptLines := content
	if lines := strings.Split(content, "\n"); len(lines) > tailLines {
		promptLines = strings.Join(lines[len(lines)-tailLines:], "\n")
	}
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.Contains(promptLines, p) {
			return p, true
		}
	}
	return "", false
}
