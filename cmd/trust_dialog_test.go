package cmd

import "testing"

func TestTrustDialogKeyFixtures(t *testing.T) {
	tests := []struct {
		name       string
		buffer     string
		wantKey    string
		wantAccept bool
		wantDialog bool
	}{
		{"claude no initially selected", "Quick safety check\n ❯ No, exit\n   Yes, I trust this folder\nEnter to confirm · Esc to cancel", "\x1b[B", false, true},
		{"trust option is above the selected exit", "Do you trust this folder?\n   Trust this folder\n ❯ No, exit", "\x1b[A", false, true},
		{"claude yes selected", "Quick safety check\n   No, exit\n ❯ Yes, I trust this folder\nEnter to confirm · Esc to cancel", "", true, true},
		{"numbered trust option selected", "Workspace trust\n ❯ 1. Trust this folder\n   2. Exit\nEnter to confirm", "", true, true},
		{"codex positive option selected", "Do you trust the authors of this project?\n ❯ Yes, trust this folder (trust)\n   No, exit", "", true, true},
		{"no dialog", "Claude Code\nbypass permissions\n>", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeTrustDialog(tt.buffer); got != tt.wantDialog {
				t.Fatalf("dialog = %v, want %v", got, tt.wantDialog)
			}
			key, accept := trustDialogKey(tt.buffer)
			if key != tt.wantKey || accept != tt.wantAccept {
				t.Fatalf("trustDialogKey = (%q, %v), want (%q, %v)", key, accept, tt.wantKey, tt.wantAccept)
			}
		})
	}
}

func TestTrustDialogNoSelectionDoesNotAccept(t *testing.T) {
	if key, accept := trustDialogKey("❯ No, exit\n  Yes, I trust this folder"); key != "\x1b[B" || accept {
		t.Fatalf("unexpected choice (%q, %v)", key, accept)
	}
}

func TestLooksLikeCommandPrompt(t *testing.T) {
	if !looksLikeCommandPrompt("Claude exited\nC:\\Users\\me\\project>") {
		t.Fatal("expected command prompt detection")
	}
	if looksLikeCommandPrompt("Claude Code\nbypass permissions\n>") {
		t.Fatal("mistook Claude prompt for command prompt")
	}
}
