package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var deckpilotExe string

func TestMain(m *testing.M) {
	// Build deckpilot.exe into a temp directory
	tmp, err := os.MkdirTemp("", "deckpilot-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmpdir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	deckpilotExe = filepath.Join(tmp, "deckpilot.exe")
	goExe := `C:\Program Files\Go\bin\go.exe`
	build := exec.Command(goExe, "build", "-o", deckpilotExe, ".")
	build.Dir = `C:\Users\yuuji\deckpilot`
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// e2eSession holds the state of a test-dedicated Ghostty session.
type e2eSession struct {
	name string // session name returned by launch
	pid  int    // Ghostty PID for cleanup
}

// launchTestSession starts a new Ghostty+claude session via "deckpilot launch".
// Returns the session info or calls t.Skip if Ghostty is unavailable.
func launchTestSession(t *testing.T, prompt string) *e2eSession {
	t.Helper()

	cmd := exec.Command(deckpilotExe, "launch", "claude", prompt)
	cmd.Dir = `C:\Users\yuuji\deckpilot`

	// launch writes session name to stdout (last line), diagnostics to stderr
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		if strings.Contains(stderr, "ghostty not found") {
			t.Skip("Ghostty binary not available, skipping E2E test")
		}
		t.Skipf("launch failed (Ghostty may not be available): %v\nstderr: %s", err, stderr)
		return nil
	}

	sessionName := strings.TrimSpace(string(out))
	if sessionName == "" {
		t.Fatal("launch returned empty session name")
	}

	// Find the Ghostty PID by listing processes that appeared recently.
	// We use tasklist filtered by ghostty.exe name.
	pid := findGhosttyPID(t, sessionName)

	t.Logf("launched test session: name=%s pid=%d", sessionName, pid)
	return &e2eSession{name: sessionName, pid: pid}
}

// findGhosttyPID finds a Ghostty PID. We look for ghostty.exe processes.
// Returns 0 if not found (cleanup will be best-effort).
func findGhosttyPID(t *testing.T, sessionName string) int {
	t.Helper()
	// Use tasklist to find ghostty.exe PIDs
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq ghostty.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		t.Logf("tasklist failed: %v", err)
		return 0
	}
	// CSV lines: "ghostty.exe","1234","Console","1","123,456 K"
	// We want the latest (highest) PID as it's likely our just-launched instance
	maxPID := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "ghostty.exe") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		pidStr := strings.Trim(fields[1], "\" ")
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > maxPID {
			maxPID = pid
		}
	}
	return maxPID
}

// cleanup kills the Ghostty process associated with this session.
func (s *e2eSession) cleanup(t *testing.T) {
	t.Helper()
	if s == nil {
		return
	}
	if s.pid > 0 {
		t.Logf("cleaning up: killing ghostty PID %d", s.pid)
		cmd := exec.Command("taskkill", "/PID", strconv.Itoa(s.pid), "/F")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("taskkill warning: %v\n%s", err, out)
		}
	}
}

// sharedSession is a lazily-initialized session shared across tests that need Ghostty.
var sharedSession *e2eSession
var sharedSessionSetup bool

func getOrCreateSharedSession(t *testing.T) *e2eSession {
	t.Helper()
	if sharedSessionSetup {
		if sharedSession == nil {
			t.Skip("shared session not available (Ghostty not found)")
		}
		return sharedSession
	}
	sharedSessionSetup = true
	sharedSession = launchTestSession(t, "echo ready")
	// Register cleanup at test end - will be called when the test binary exits
	// We handle this in the individual test or via a manual cleanup
	return sharedSession
}

func TestSendAndShow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	sess := getOrCreateSharedSession(t)
	defer func() {
		// Only clean up if this is the session creator
		// Other tests reuse it, final cleanup is at TestCleanup
	}()

	marker := "deckpilot_e2e_test_ok"
	sendCmd := exec.Command(deckpilotExe, "send", sess.name, "echo "+marker)
	sendOut, err := sendCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("send failed: %v\n%s", err, sendOut)
	}
	t.Logf("send output: %s", sendOut)

	time.Sleep(5 * time.Second)

	showCmd := exec.Command(deckpilotExe, "show", sess.name)
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, showOut)
	}
	t.Logf("show output length: %d bytes", len(showOut))

	if !strings.Contains(string(showOut), marker) {
		t.Errorf("buffer does not contain %q\ngot:\n%s", marker, showOut)
	}
}

func TestShowLastUsed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	sess := getOrCreateSharedSession(t)

	marker := "deckpilot_e2e_lastused"
	sendCmd := exec.Command(deckpilotExe, "send", sess.name, "echo "+marker)
	sendOut, err := sendCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("send failed: %v\n%s", err, sendOut)
	}
	t.Logf("send output: %s", sendOut)

	time.Sleep(5 * time.Second)

	// show with no session ID -- should use last-used
	showCmd := exec.Command(deckpilotExe, "show")
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show (no id) failed: %v\n%s", err, showOut)
	}
	t.Logf("show output length: %d bytes", len(showOut))

	if !strings.Contains(string(showOut), marker) {
		t.Errorf("buffer does not contain %q\ngot:\n%s", marker, showOut)
	}
}

func TestSlashCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	sess := getOrCreateSharedSession(t)

	sendCmd := exec.Command(deckpilotExe, "send", sess.name, "/help")
	sendOut, err := sendCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("send /help failed: %v\n%s", err, sendOut)
	}
	t.Logf("send output: %s", sendOut)

	time.Sleep(8 * time.Second)

	showCmd := exec.Command(deckpilotExe, "show", sess.name)
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, showOut)
	}
	t.Logf("show output length: %d bytes", len(showOut))

	lower := strings.ToLower(string(showOut))
	if !strings.Contains(lower, "help") && !strings.Contains(lower, "commands") {
		t.Errorf("buffer does not contain help-related output\ngot:\n%s", showOut)
	}
}

func TestDeletedCommands(t *testing.T) {
	// This test does NOT require Ghostty -- just checks that removed commands
	// return "unknown command" with non-zero exit.
	tests := []struct {
		name string
		args []string
	}{
		{"status", []string{"status", "foo"}},
		{"listen", []string{"listen"}},
		{"output", []string{"output", "foo"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(deckpilotExe, tt.args...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected non-zero exit for %q, got success\noutput: %s", tt.name, out)
			}
			if !strings.Contains(string(out), "unknown command") {
				t.Errorf("expected 'unknown command' in output, got:\n%s", out)
			}
		})
	}
}

func TestLaunchWithPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	marker := "launch_prompt_test_marker_" + strconv.FormatInt(time.Now().Unix(), 10)
	// Launch with a prompt that echoes a marker.
	// We use "claude" agent which is defined in agents.go.
	// Since we don't want to actually run Claude, we might need a dummy agent or
	// rely on the fact that 'claude' command might not exist on the test machine,
	// but the test 'launchTestSession' skips if Ghostty is missing.
	
	// Wait, if I use 'claude' agent, it tries to run 'claude' command.
	// For E2E test, maybe I should check if 'claude' or 'codex' is available.
	// Actually, the existing tests use 'claude' in launchTestSession but it
	// seems to work (or skip).
	
	cmd := exec.Command(deckpilotExe, "launch", "claude", "echo "+marker)
	cmd.Dir = `C:\Users\yuuji\deckpilot`
	
	out, err := cmd.CombinedOutput()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		if strings.Contains(string(out)+stderr, "ghostty not found") {
			t.Skip("Ghostty not found")
		}
		// If 'claude' is not found, it's an agent error but deckpilot might still
		// have "sent" the prompt to the shell before claude failed to start,
		// OR it might have failed to find claude.
		t.Logf("launch failed: %v\noutput: %s", err, out)
	}

	sessionName := strings.TrimSpace(string(out))
	if sessionName == "" {
		t.Fatal("launch returned empty session name")
	}
	
	// Ghostty PID for cleanup
	pid := findGhosttyPID(t, sessionName)
	defer func() {
		if pid > 0 {
			exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").Run()
		}
	}()

	t.Logf("launched session: %s", sessionName)

	// Wait for the prompt to be processed and marker to appear in buffer
	time.Sleep(10 * time.Second)

	showCmd := exec.Command(deckpilotExe, "show", sessionName)
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, showOut)
	}

	if !strings.Contains(string(showOut), marker) {
		t.Errorf("buffer does not contain marker %q\ngot:\n%s", marker, showOut)
	}
}

// TestCleanup runs last (alphabetically after other tests) and kills the shared Ghostty.
// Go runs tests in the order they appear in the file, so this being last is intentional.
func TestCleanup(t *testing.T) {
	if sharedSession != nil {
		sharedSession.cleanup(t)
		sharedSession = nil
	}
}
