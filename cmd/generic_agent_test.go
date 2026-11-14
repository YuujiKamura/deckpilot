package cmd

import (
	"errors"
	"testing"
	"time"
)

const agyTrustScreen = `Accessing workspace:

C:\work\proj

Do you trust the contents of this project?

Antigravity CLI requires permission to read, edit, and execute files here.

> Yes, I trust this folder
  No, exit

  ↑/↓ Navigate · enter Confirm
`

func TestGenericAgentSplitsCommandLine(t *testing.T) {
	a, ok := genericAgent("aider --model x")
	if !ok || a.Cmd != "aider" || len(a.Args) != 2 || a.Args[1] != "x" || !a.Generic || a.ReadyStr != "" {
		t.Fatalf("unexpected generic agent: %+v ok=%v", a, ok)
	}
	if _, ok := genericAgent("   "); ok {
		t.Fatal("blank command line must be rejected")
	}
}

func TestTrustDialogWithAsciiCursor(t *testing.T) {
	if !looksLikeTrustDialog(agyTrustScreen) {
		t.Fatal("agy trust screen not recognised as a trust dialog")
	}
	key, selected := trustDialogKey(agyTrustScreen)
	if key != "" || !selected {
		t.Fatalf("trust option is selected with an ASCII cursor: key=%q selected=%v", key, selected)
	}
}

func TestChoiceFooterIsAMenu(t *testing.T) {
	unnumbered := "Pick a theme\n\n> Dark\n  Light\n\n  ↑/↓ Navigate · enter Confirm\n"
	if !looksLikeMenu(unnumbered) {
		t.Fatal("an unnumbered selection prompt must count as a menu")
	}
	if !looksLikeMenu("Overwrite the file? (y/n)\n") {
		t.Fatal("a y/n question must count as a menu")
	}
	if looksLikeMenu("Welcome.\nPress enter to send a message.\n\n> \n") {
		t.Fatal("an input box is not a menu")
	}
}

// screens feeds waitForReady a fixed sequence, repeating the last one.
func screens(t *testing.T, seq ...string) {
	t.Helper()
	oldShow, oldPoll := daemonShowFn, readyPollInterval
	readyPollInterval = time.Millisecond
	i := 0
	daemonShowFn = func(session, mode, caller string) (string, string, error) {
		s := seq[i]
		if i < len(seq)-1 {
			i++
		}
		return s, "active", nil
	}
	t.Cleanup(func() { daemonShowFn, readyPollInterval = oldShow, oldPoll })
}

func TestUnknownCliIsReadyWhenScreenSettles(t *testing.T) {
	agent, _ := genericAgent("somecli")
	screens(t, "C:\\w>cmd /k somecli", "loading", "somecli v1\n\ntype here: ")
	d, err := waitForReady("s", agent, 2*time.Second)
	if err != nil || !d.ReadyMatched {
		t.Fatalf("settled screen should be ready: err=%v diag=%+v", err, d)
	}
}

func TestUnknownCliNotReadyBeforeItDrawsAnything(t *testing.T) {
	agent, _ := genericAgent("somecli")
	screens(t, "C:\\w>cmd /k somecli")
	if _, err := waitForReady("s", agent, 50*time.Millisecond); err == nil {
		t.Fatal("a screen that never changed after the launch command is not ready")
	}
}

func TestUnknownCliMenuIsReportedAndLeftOpen(t *testing.T) {
	agent, _ := genericAgent("somecli")
	screens(t, "C:\\w>cmd /k somecli", "Pick a theme\n\n> Dark\n  Light\n\n  ↑/↓ Navigate · enter Confirm\n")
	_, err := waitForReady("s", agent, 2*time.Second)
	if !errors.Is(err, errSessionLeftOpen) {
		t.Fatalf("menu must stop the launch and leave the session open, got %v", err)
	}
}
