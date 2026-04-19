//go:build windows

package pipe

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnrichFromSessionFile_PullsHWND reproduces the issue #29 bug and
// verifies the fix: discoverFromProcesses used to return a Session
// with empty HWND, which meant IsHungAppWindow was always called with
// 0 and every hang was invisible to the Watcher.
//
// The fix makes discoverFromProcesses enrich the Session from the
// well-known ghostty session file so HWND is populated. This test
// exercises the enrich helper directly with a scripted session file.
func TestEnrichFromSessionFile_PullsHWND(t *testing.T) {
	// Arrange: fake LOCALAPPDATA with a ghostty session file.
	tempAppData := t.TempDir()
	runtime := "winui3"
	sessionDir := filepath.Join(tempAppData,
		"ghostty", "control-plane", runtime, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const (
		name = "ghostty-12345"
		pid  = 12345
		hwnd = "0xF01974"
	)
	sessionFile := filepath.Join(sessionDir, name+"-12345.session")
	content := "session_name=" + name + "\n" +
		"pid=12345\n" +
		"hwnd=" + hwnd + "\n" +
		"pipe_path=\\\\.\\pipe\\ghostty-winui3-ghostty-12345-12345\n"
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	t.Setenv("LOCALAPPDATA", tempAppData)

	base := Session{
		Name:       name,
		PID:        pid,
		PipePath:   `\\.\pipe\ghostty-winui3-ghostty-12345-12345`,
		AppRuntime: runtime,
	}

	// Act.
	got, err := enrichFromSessionFile(base, runtime)
	if err != nil {
		t.Fatalf("enrichFromSessionFile: %v", err)
	}

	// Assert — enrichment fills HWND without disturbing base fields.
	if got.HWND != hwnd {
		t.Errorf("HWND=%q, want %q", got.HWND, hwnd)
	}
	if got.SessionFile == "" {
		t.Error("SessionFile should have been populated")
	}
	if got.Name != name {
		t.Errorf("base Name was overwritten: %q", got.Name)
	}
	if got.PID != pid {
		t.Errorf("base PID was overwritten: %d", got.PID)
	}
	if got.PipePath != base.PipePath {
		t.Errorf("base PipePath was overwritten: %q", got.PipePath)
	}
}

// TestEnrichFromSessionFile_NoLocalAppData returns the base Session
// unchanged (plus an error) when LOCALAPPDATA is unset. Callers rely
// on this to keep hang-blind but pipe-functional operation.
func TestEnrichFromSessionFile_NoLocalAppData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", "")

	base := Session{Name: "ghostty-1", PID: 1}
	got, err := enrichFromSessionFile(base, "winui3")
	if err == nil {
		t.Error("expected error when LOCALAPPDATA unset")
	}
	if got.Name != "ghostty-1" || got.PID != 1 {
		t.Error("base Session should survive the failed enrichment")
	}
	if got.HWND != "" {
		t.Errorf("HWND should remain empty, got %q", got.HWND)
	}
}

// TestEnrichFromSessionFile_MissingFile same as above when the session
// file doesn't exist at the expected path (e.g. Ghostty never wrote
// one, or the name/pid combo is off).
func TestEnrichFromSessionFile_MissingFile(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())

	base := Session{Name: "ghostty-99", PID: 99, PipePath: "p"}
	got, err := enrichFromSessionFile(base, "winui3")
	if err == nil {
		t.Error("expected error when session file missing")
	}
	if got.HWND != "" {
		t.Errorf("HWND should remain empty, got %q", got.HWND)
	}
	if got.PipePath != "p" {
		t.Errorf("base PipePath was overwritten")
	}
}

// TestEnrichFromSessionFile_PreservesNonEmptyBase verifies that the
// enrichment never overwrites base fields that were already set.
func TestEnrichFromSessionFile_PreservesNonEmptyBase(t *testing.T) {
	tempAppData := t.TempDir()
	runtime := "winui3"
	sessionDir := filepath.Join(tempAppData,
		"ghostty", "control-plane", runtime, "sessions")
	_ = os.MkdirAll(sessionDir, 0o755)
	sessionFile := filepath.Join(sessionDir, "ghostty-42-42.session")
	_ = os.WriteFile(sessionFile, []byte(
		"hwnd=0xDEADBEEF\nws_url=ws://from-file/\n"), 0o644)
	t.Setenv("LOCALAPPDATA", tempAppData)

	base := Session{
		Name:  "ghostty-42",
		PID:   42,
		HWND:  "0xPREEXISTING",
		WsURL: "ws://from-base/",
	}
	got, err := enrichFromSessionFile(base, runtime)
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if got.HWND != "0xPREEXISTING" {
		t.Errorf("base HWND was overwritten: %q", got.HWND)
	}
	if got.WsURL != "ws://from-base/" {
		t.Errorf("base WsURL was overwritten: %q", got.WsURL)
	}
}
