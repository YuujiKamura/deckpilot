package cmd

import "strings"

// agentApprovalPatterns maps agent type → approval prompt patterns.
// Each entry contains only strings that are distinctive interactive prompts
// for that agent — avoid generic words that appear in normal output.
var agentApprovalPatterns = map[string][]string{
	"claude": {
		"Action Required",
		"Enter to select",
		"Y/n",
		"Allow",
		"trust",
		"Waiting",
	},
	"gemini": {
		// Gemini outputs "Allow", "Waiting" etc. in normal status text,
		// so only use highly distinctive interactive prompts here.
		"Y/n",
		"(y/N)",
		"Allow execution of",
		"Accept",
		"実行を許可しますか",
		"承認",
		"許可",
	},
	"codex": {
		"Would you like to run",
		"Press enter to confirm",
		"Yes, proceed",
		"Y/n",
		"Action Required",
	},
}

// approvalPatterns is the legacy flat list (claude patterns).
// Retained so existing code and tests that reference it continue to work.
var approvalPatterns = agentApprovalPatterns["claude"]

// DetectApprovalPrompt checks the last 15 lines of content for any approval
// prompt pattern using claude-compatible patterns.
// Kept for backward compatibility; prefer DetectApprovalPromptForAgent.
func DetectApprovalPrompt(content string) (matched string, found bool) {
	return DetectApprovalPromptForAgent(content, "claude")
}

// DetectApprovalPromptForAgent checks the last 30 lines of content for an
// approval prompt pattern specific to the given agent type.
// Unknown agent names fall back to claude patterns.
func DetectApprovalPromptForAgent(content, agent string) (matched string, found bool) {
	patterns, ok := agentApprovalPatterns[agent]
	if !ok {
		patterns = agentApprovalPatterns["claude"]
	}

	promptLines := content
	if lines := strings.Split(content, "\n"); len(lines) > 30 {
		promptLines = strings.Join(lines[len(lines)-30:], "\n")
	}
	for _, p := range patterns {
		if strings.Contains(promptLines, p) {
			return p, true
		}
	}
	return "", false
}

// inferAgentFromBuffer tries to detect which agent is running by scanning
// the last 100 lines of buffer output for agent-specific signatures.
// Returns "" when inconclusive.
func inferAgentFromBuffer(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}
	tail := strings.Join(lines, "\n")

	// Gemini CLI banner / prompt markers
	if strings.Contains(tail, "Gemini") || strings.Contains(tail, "gemini>") {
		return "gemini"
	}
	// Codex CLI marker
	if strings.Contains(tail, "codex>") || strings.Contains(tail, "Codex") {
		return "codex"
	}
	return ""
}
