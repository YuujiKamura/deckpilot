package cmd

import (
	"strings"
	"testing"
)

// TestDetectApprovalPrompt_Patterns verifies every claude pattern is detected when present
// in the last 15 lines of content.
func TestDetectApprovalPrompt_Patterns(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		found   bool
	}{
		// --- positive cases: each of the 6 claude patterns ---
		{
			name:    "ActionRequired",
			content: "some output\nAction Required: confirm\n",
			want:    "Action Required",
			found:   true,
		},
		{
			name:    "EnterToSelect",
			content: "list of items\nPress Enter to select\n",
			want:    "Enter to select",
			found:   true,
		},
		{
			name:    "YesNo",
			content: "Do you want to proceed? (Y/n)\n",
			want:    "Y/n",
			found:   true,
		},
		{
			name:    "Allow",
			content: "Allow edit to file.ts?\n",
			want:    "Allow",
			found:   true,
		},
		{
			name:    "Trust",
			content: "Do you trust the authors of this workspace? (trust)\n",
			want:    "trust",
			found:   true,
		},
		{
			name:    "Waiting",
			content: "Waiting for user input...\n",
			want:    "Waiting",
			found:   true,
		},

		// --- negative cases: no pattern present ---
		{
			name:    "NoMatch_empty",
			content: "",
			want:    "",
			found:   false,
		},
		{
			name:    "NoMatch_plain",
			content: "Just normal output\nno triggers here\n",
			want:    "",
			found:   false,
		},
		{
			name:    "NoMatch_partial",
			// "allow" lowercase does not match "Allow"
			content: "allow me to explain\n",
			want:    "",
			found:   false,
		},

		// --- first pattern wins when multiple match ---
		{
			name:    "MultiplePatterns_firstWins",
			content: "Action Required: confirm\nY/n prompt\n",
			want:    "Action Required",
			found:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, found := DetectApprovalPrompt(tt.content)
			if found != tt.found {
				t.Errorf("found=%v, want %v (content=%q)", found, tt.found, tt.content)
			}
			if matched != tt.want {
				t.Errorf("matched=%q, want %q", matched, tt.want)
			}
		})
	}
}

// TestDetectApprovalPrompt_Last15Lines checks the sliding-window boundary:
// patterns beyond the last 15 lines must NOT be detected.
func TestDetectApprovalPrompt_Last15Lines(t *testing.T) {
	var lines []string
	lines = append(lines, "Action Required: buried far back")
	for i := 0; i < 20; i++ {
		lines = append(lines, "filler line")
	}
	content := strings.Join(lines, "\n")

	_, found := DetectApprovalPrompt(content)
	if found {
		t.Error("expected no match: pattern is outside last-15-line window")
	}
}

// TestDetectApprovalPrompt_ExactlyAt15thLine ensures a pattern on exactly the
// 15th-from-last line IS detected (boundary inclusive).
func TestDetectApprovalPrompt_ExactlyAt15thLine(t *testing.T) {
	var lines []string
	lines = append(lines, "Action Required: at boundary")
	for i := 0; i < 14; i++ {
		lines = append(lines, "filler")
	}
	content := strings.Join(lines, "\n")

	matched, found := DetectApprovalPromptForAgent(content, "claude")
	if !found {
		t.Error("expected match: pattern is exactly at the 15-line boundary")
	}
	if matched != "Action Required" {
		t.Errorf("matched=%q, want %q", matched, "Action Required")
	}
}

// TestDetectApprovalPrompt_ShortContent verifies behaviour when content has
// fewer than 15 lines (the whole content is checked).
func TestDetectApprovalPrompt_ShortContent(t *testing.T) {
	content := "line1\nY/n\nline3"
	matched, found := DetectApprovalPrompt(content)
	if !found {
		t.Error("expected match in short content")
	}
	if matched != "Y/n" {
		t.Errorf("matched=%q, want Y/n", matched)
	}
}

// TestApprovalPatterns_List ensures the exported approvalPatterns slice contains
// all 6 expected claude patterns and no duplicates.
func TestApprovalPatterns_List(t *testing.T) {
	expected := []string{
		"Action Required",
		"Enter to select",
		"Y/n",
		"Allow",
		"trust",
		"Waiting",
	}
	if len(approvalPatterns) != len(expected) {
		t.Fatalf("approvalPatterns length=%d, want %d", len(approvalPatterns), len(expected))
	}
	seen := make(map[string]bool)
	for _, p := range approvalPatterns {
		if seen[p] {
			t.Errorf("duplicate pattern: %q", p)
		}
		seen[p] = true
	}
	for _, e := range expected {
		if !seen[e] {
			t.Errorf("missing expected pattern: %q", e)
		}
	}
}

// TestDetectApprovalPromptForAgent_Gemini verifies gemini patterns match only
// distinctive interactive prompts and do NOT trigger on "Allow" or "Waiting"
// which appear in normal Gemini output.
func TestDetectApprovalPromptForAgent_Gemini(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		found   bool
	}{
		{
			name:    "Gemini_YesNo_yn",
			content: "Proceed? (Y/n)\n",
			want:    "Y/n",
			found:   true,
		},
		{
			name:    "Gemini_YesNo_yN",
			content: "Continue? (y/N)\n",
			want:    "(y/N)",
			found:   true,
		},
		// "Allow" must NOT match for Gemini — it appears in normal status output.
		{
			name:    "Gemini_NoMatch_Allow",
			content: "Allow me to analyze this file...\n",
			want:    "",
			found:   false,
		},
		// "Waiting" must NOT match for Gemini — common status text.
		{
			name:    "Gemini_NoMatch_Waiting",
			content: "Waiting for response...\nGenerating...\n",
			want:    "",
			found:   false,
		},
		{
			name:    "Gemini_NoMatch_plain",
			content: "Analyzing your request now.\n",
			want:    "",
			found:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, found := DetectApprovalPromptForAgent(tt.content, "gemini")
			if found != tt.found {
				t.Errorf("found=%v, want %v (content=%q)", found, tt.found, tt.content)
			}
			if matched != tt.want {
				t.Errorf("matched=%q, want %q", matched, tt.want)
			}
		})
	}
}

// TestDetectApprovalPromptForAgent_Codex verifies codex-specific patterns.
func TestDetectApprovalPromptForAgent_Codex(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		found   bool
	}{
		{
			name:    "Codex_WouldYouLikeToRun",
			content: "Would you like to run this command?\n",
			want:    "Would you like to run",
			found:   true,
		},
		{
			name:    "Codex_PressEnterToConfirm",
			content: "Press enter to confirm the action.\n",
			want:    "Press enter to confirm",
			found:   true,
		},
		{
			name:    "Codex_YesProceed",
			content: "Yes, proceed with deletion?\n",
			want:    "Yes, proceed",
			found:   true,
		},
		{
			name:    "Codex_YesNo",
			content: "Apply changes? (Y/n)\n",
			want:    "Y/n",
			found:   true,
		},
		{
			name:    "Codex_ActionRequired",
			content: "Action Required: confirm execution\n",
			want:    "Action Required",
			found:   true,
		},
		{
			name:    "Codex_NoMatch",
			content: "Running test suite...\n",
			want:    "",
			found:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, found := DetectApprovalPromptForAgent(tt.content, "codex")
			if found != tt.found {
				t.Errorf("found=%v, want %v (content=%q)", found, tt.found, tt.content)
			}
			if matched != tt.want {
				t.Errorf("matched=%q, want %q", matched, tt.want)
			}
		})
	}
}

// TestDetectApprovalPromptForAgent_UnknownFallback confirms an unknown agent
// name falls back to claude patterns (not a panic or empty match).
func TestDetectApprovalPromptForAgent_UnknownFallback(t *testing.T) {
	content := "Action Required: review change\n"
	matched, found := DetectApprovalPromptForAgent(content, "unknown-agent-xyz")
	if !found {
		t.Error("expected fallback to claude patterns for unknown agent")
	}
	if matched != "Action Required" {
		t.Errorf("matched=%q, want %q", matched, "Action Required")
	}
}

// TestInferAgentFromBuffer verifies buffer-based agent detection heuristics.
func TestInferAgentFromBuffer(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "Gemini_from_banner",
			content: "Welcome to Gemini CLI\n> ",
			want:    "gemini",
		},
		{
			name:    "Gemini_from_prompt",
			content: "gemini> analyze this\n",
			want:    "gemini",
		},
		{
			name:    "Codex_from_banner",
			content: "Codex v0.122\n> ",
			want:    "codex",
		},
		{
			name:    "Inconclusive_returns_empty",
			content: "some random terminal output\n",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferAgentFromBuffer(tt.content)
			if got != tt.want {
				t.Errorf("inferAgentFromBuffer=%q, want %q", got, tt.want)
			}
		})
	}
}
