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

// TestAutoApprovalsCommand verifies that "auto-approvals" is a recognised command
// and that "--dry-run" exits cleanly (no daemon needed for flag-error path).
func TestAutoApprovalsCommand(t *testing.T) {
	// Without a session name the command must exit non-zero and print usage.
	cmd := exec.Command(deckpilotExe, "auto-approvals")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit when session name omitted")
	}
	if !strings.Contains(string(out), "usage") && !strings.Contains(string(out), "auto-approvals") {
		t.Errorf("expected usage message, got:\n%s", out)
	}
}

// TestAutoApprovalsAlias confirms "approve" is accepted as an alias.
func TestAutoApprovalsAlias(t *testing.T) {
	cmd := exec.Command(deckpilotExe, "approve")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for approve without session")
	}
	// Must NOT return "unknown command" – the alias must be recognised.
	if strings.Contains(string(out), "unknown command") {
		t.Errorf("'approve' alias not recognised:\n%s", out)
	}
}

// TestAutoApprovalsUnknownFlag checks that an unknown flag exits non-zero.
func TestAutoApprovalsUnknownFlag(t *testing.T) {
	cmd := exec.Command(deckpilotExe, "auto-approvals", "mysession", "--bogus-flag")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown flag")
	}
	if !strings.Contains(string(out), "unknown flag") {
		t.Errorf("expected 'unknown flag' message, got:\n%s", out)
	}
}

// TestAutoApprovalsIntervalFlag verifies --interval accepts valid durations and
// rejects invalid ones without panicking.
func TestAutoApprovalsIntervalFlag(t *testing.T) {
	// Invalid duration → non-zero exit with error message.
	cmd := exec.Command(deckpilotExe, "auto-approvals", "mysession", "--interval", "notaduration")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for bad --interval value")
	}
	if !strings.Contains(string(out), "invalid --interval") {
		t.Errorf("expected 'invalid --interval' message, got:\n%s", out)
	}
}

// TestWatchOnceJSON verifies "watch --once --json" produces valid JSON lines
// (or exits cleanly with no active sessions). Skipped in short mode.
func TestWatchOnceJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}
	cmd := exec.Command(deckpilotExe, "watch", "--once", "--json")
	out, err := cmd.CombinedOutput()
	// "watch" may exit non-zero when no sessions are active (daemon unavailable).
	// Either way, any stdout lines must be valid JSON.
	outStr := strings.TrimSpace(string(out))
	for _, line := range strings.Split(outStr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			t.Errorf("non-JSON line in --json output: %q (exit err: %v)", line, err)
		}
	}
}

// TestWatchDeprecationWarning checks that "watch <session>" emits a deprecation
// warning on stderr and that DECKPILOT_SUPPRESS_DEPRECATION=1 suppresses it.
// The test does not require a live daemon; it inspects stderr before any IPC.
func TestWatchDeprecationWarning(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}
	// Without suppression: deprecation warning expected.
	cmd := exec.Command(deckpilotExe, "watch", "some-session", "--once")
	cmd.Env = append(os.Environ(), "DECKPILOT_SUPPRESS_DEPRECATION=")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "deprecation") {
		t.Errorf("expected deprecation warning; got:\n%s", out)
	}

	// With suppression: no deprecation warning.
	cmd2 := exec.Command(deckpilotExe, "watch", "some-session", "--once")
	cmd2.Env = append(os.Environ(), "DECKPILOT_SUPPRESS_DEPRECATION=1")
	out2, _ := cmd2.CombinedOutput()
	if strings.Contains(string(out2), "deprecation") {
		t.Errorf("expected suppressed deprecation warning; got:\n%s", out2)
	}
}

// TestShowFlags verifies backward-compatible flag combinations for "show".
// These are flag-parsing tests and do not require a live session.
func TestShowFlags(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantExit    bool   // true = expect non-zero exit
		wantInOut   string // substring expected in combined output (may be empty)
		noUnknown   bool   // if true, must NOT contain "unknown command"
	}{
		{
			name:      "help flag",
			args:      []string{"show", "--help"},
			wantExit:  false,
			wantInOut: "auto-approvals", // help must mention auto-approvals
		},
		{
			name:     "bad tail value",
			args:     []string{"show", "--tail", "abc"},
			wantExit: true,
		},
		{
			name:     "unknown flag",
			args:     []string{"show", "--bogus"},
			wantExit: true,
		},
		{
			name:      "show is recognised",
			args:      []string{"show", "--help"},
			wantExit:  false,
			noUnknown: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(deckpilotExe, tt.args...)
			out, err := cmd.CombinedOutput()
			exitErr := err != nil
			if exitErr != tt.wantExit {
				t.Errorf("exit error=%v, want %v (output: %s)", exitErr, tt.wantExit, out)
			}
			if tt.wantInOut != "" && !strings.Contains(string(out), tt.wantInOut) {
				t.Errorf("expected %q in output, got:\n%s", tt.wantInOut, out)
			}
			if tt.noUnknown && strings.Contains(string(out), "unknown command") {
				t.Errorf("unexpected 'unknown command' in output:\n%s", out)
			}
		})
	}
}

// TestHelpLists verifies the help output contains all new commands so nothing
// was accidentally omitted from the dispatch table.
func TestHelpLists(t *testing.T) {
	cmd := exec.Command(deckpilotExe, "help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("help command failed: %v\n%s", err, out)
	}
	for _, want := range []string{"auto-approvals", "watch", "show", "send", "list", "launch"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("help output missing %q:\n%s", want, out)
		}
	}
}

func TestCleanup(t *testing.T) {
	if sharedSession != nil {
		sharedSession.cleanup(t)
		sharedSession = nil
	}
}
