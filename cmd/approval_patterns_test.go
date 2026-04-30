package cmd

import (
	"strings"
	"testing"
)

// TestDetectApprovalPrompt_Patterns verifies every pattern is detected when present
// in the last 15 lines of content.
func TestDetectApprovalPrompt_Patterns(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		found   bool
	}{
		// --- positive cases: each of the 6 patterns ---
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
	// Build a buffer where "Action Required" is on line 1 (far back),
	// preceded by 20 filler lines so it sits outside the last-15 window.
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
	// 14 filler lines after the pattern line → pattern is exactly line[0] of the
	// 15-line tail window.
	var lines []string
	lines = append(lines, "Action Required: at boundary")
	for i := 0; i < 14; i++ {
		lines = append(lines, "filler")
	}
	content := strings.Join(lines, "\n")

	matched, found := DetectApprovalPrompt(content)
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
// all 6 expected patterns and no duplicates.
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
