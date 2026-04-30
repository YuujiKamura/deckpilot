package cmd

import "strings"

// approvalPatterns are the prompt strings that trigger auto-approval.
// Extracted into a shared helper so both auto-approvals and watch can use
// identical detection logic without duplication.
var approvalPatterns = []string{
	"Action Required",
	"Enter to select",
	"Y/n",
	"Allow",
	"trust",
	"Waiting",
}

// DetectApprovalPrompt checks the last 15 lines of content for any approval
// prompt pattern. Returns the matched pattern name and true when found.
func DetectApprovalPrompt(content string) (matched string, found bool) {
	// Only check last 15 lines to avoid false positives from log output
	promptLines := content
	if lines := strings.Split(content, "\n"); len(lines) > 15 {
		promptLines = strings.Join(lines[len(lines)-15:], "\n")
	}
	for _, p := range approvalPatterns {
		if strings.Contains(promptLines, p) {
			return p, true
		}
	}
	return "", false
}
