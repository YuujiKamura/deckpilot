package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var deckpilotExe string

// projectDir returns the directory containing this test file.
func projectDir() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Dir(f)
}

// findGo locates the Go compiler.
func findGo() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Go", "bin", "go.exe"),
		filepath.Join(os.Getenv("USERPROFILE"), "go", "bin", "go.exe"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "go" // fallback
}

func TestMain(m *testing.M) {
	// Build deckpilot.exe into a temp directory
	tmp, err := os.MkdirTemp("", "deckpilot-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmpdir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	deckpilotExe = filepath.Join(tmp, "deckpilot.exe")
	goExe := findGo()
	build := exec.Command(goExe, "build", "-o", deckpilotExe, ".")
	build.Dir = projectDir()
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
func launchTestSession(t *testing.T, prompt string) *e2eSession {
	t.Helper()

	cmd := exec.Command(deckpilotExe, "launch", "claude", prompt)
	cmd.Dir = projectDir()

	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Skipf("launch failed: %v\nstderr: %s", err, stderr)
		return nil
	}

	sessionName := strings.TrimSpace(string(out))
	if sessionName == "" {
		t.Fatal("launch returned empty session name")
	}

	pid := findGhosttyPID(t, sessionName)
	t.Logf("launched test session: name=%s pid=%d", sessionName, pid)
	return &e2eSession{name: sessionName, pid: pid}
}

func findGhosttyPID(t *testing.T, sessionName string) int {
	t.Helper()
	out, err := exec.Command(deckpilotExe, "list", "--json").Output()
	if err != nil {
		t.Logf("deckpilot list failed: %v", err)
		return 0
	}

	var sessions []struct {
		Name string `json:"name"`
		PID  int    `json:"pid"`
	}
	if err := json.Unmarshal(out, &sessions); err != nil {
		t.Logf("json unmarshal failed: %v", err)
		return 0
	}

	for _, s := range sessions {
		if s.Name == sessionName {
			return s.PID
		}
	}
	return 0
}

func (s *e2eSession) cleanup(t *testing.T) {
	t.Helper()
	if s == nil || s.pid <= 0 {
		return
	}
	t.Logf("cleaning up: killing ghostty PID %d", s.pid)
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(s.pid), "/F")
	_ = cmd.Run()
}

var sharedSession *e2eSession
var sharedSessionSetup bool

func getOrCreateSharedSession(t *testing.T) *e2eSession {
	t.Helper()
	if sharedSessionSetup {
		if sharedSession == nil {
			t.Skip("shared session not available")
		}
		return sharedSession
	}
	sharedSessionSetup = true
	sharedSession = launchTestSession(t, "echo ready")
	return sharedSession
}

func TestSendAndShow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}
	sess := getOrCreateSharedSession(t)
	marker := "deckpilot_e2e_test_ok"
	sendCmd := exec.Command(deckpilotExe, "send", sess.name, "echo "+marker)
	if out, err := sendCmd.CombinedOutput(); err != nil {
		t.Fatalf("send failed: %v\n%s", err, out)
	}
	time.Sleep(5 * time.Second)
	showCmd := exec.Command(deckpilotExe, "show", sess.name)
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("show failed: %v\n%s", err, showOut)
	}
	if !strings.Contains(string(showOut), marker) {
		t.Errorf("buffer does not contain %q\ngot:\n%s", marker, showOut)
	}
}

func TestDeletedCommands(t *testing.T) {
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
				t.Fatalf("expected non-zero exit for %q, got success", tt.name)
			}
			if !strings.Contains(string(out), "unknown command") {
				t.Errorf("expected 'unknown command', got:\n%s", out)
			}
		})
	}
}

func TestCleanup(t *testing.T) {
	if sharedSession != nil {
		sharedSession.cleanup(t)
		sharedSession = nil
	}
}
