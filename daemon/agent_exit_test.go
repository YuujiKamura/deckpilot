package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWatcherMarksLaunchedAgentReturningToCmdAsDead(t *testing.T) {
	home := t.TempDir()
	previous := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = previous })
	metaPath := filepath.Join(home, ".deckpilot", "launch-meta", "ghostty-42.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]string{"session_name": "ghostty-42", "agent": "codex"})
	if err := os.WriteFile(metaPath, meta, 0o600); err != nil {
		t.Fatal(err)
	}

	w := NewWatcher("ghostty-42", "pipe", "file", 42, "", nil)
	w.updateContent("Codex update finished\nC:\\work>")
	if got := w.Status(); got != "dead" {
		t.Fatalf("status = %q, want dead", got)
	}
	if got := w.Profile().LastError; got != "agent exited and returned to the command prompt" {
		t.Fatalf("exit reason = %q", got)
	}
}

func TestOrdinaryShellPromptDoesNotMarkExternalSessionDead(t *testing.T) {
	home := t.TempDir()
	previous := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = previous })
	w := NewWatcher("external-1", "pipe", "file", 1, "", nil)
	w.updateContent("C:\\work>")
	if got := w.Status(); got == "dead" {
		t.Fatal("external shell was marked dead without launch metadata")
	}
}
