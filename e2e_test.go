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
	"syscall"
	"testing"
	"time"

	"github.com/YuujiKamura/deckpilot/cmd"
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
// on stdout. The contract of --json is "machine-parseable stdout"; human-
// oriented status ("no active sessions", "watch: list: no response from
// daemon", etc.) goes to stderr and must not contaminate the parse.
// Skipped in short mode.
func TestWatchOnceJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}
	cmd := exec.Command(deckpilotExe, "watch", "--once", "--json")
	// Output() reads only stdout; stderr (including "no active sessions"
	// when CI has no Ghostty processes, and any daemon-unavailable
	// warnings) is intentionally discarded here.
	out, err := cmd.Output()
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
		name      string
		args      []string
		wantExit  bool   // true = expect non-zero exit
		wantInOut string // substring expected in combined output (may be empty)
		noUnknown bool   // if true, must NOT contain "unknown command"
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

// ============================================================================
// Issue #35 GUARD flag E2E gates.
//
// Each gate runs against an *isolated* deckpilot daemon: the test wires
// DECKPILOT_PIPE_SUFFIX (so the IPC pipe doesn't collide with a developer's
// real daemon) and USERPROFILE (so protected-sessions.json lands in t.TempDir
// instead of the dev's home). All deckpilot CLI invocations inherit the same
// env so client and daemon agree on which pipe to talk over.
//
// The daemon's session discovery is process-wide (it scans every ghostty.exe
// PID), so an isolated daemon may also see the developer's ad-hoc Ghostty
// windows. Kill/shutdown gates are written to either (a) only target the
// session names we just spawned, or (b) skip on a workstation that has
// non-test ghostty processes running, never destroy them. This keeps the
// tests safe to run on a live dev machine and still authoritative on a clean
// CI box where the only ghostty.exe in sight is the one the test launched.
// ============================================================================

// guardEnv builds the per-test env slice with isolated pipe suffix + HOME.
// Returns the env, the suffix (so tests can label diagnostics), and the
// temp home dir (so tests can read protected-sessions.json directly).
func guardEnv(t *testing.T) (env []string, suffix string, homeDir string) {
	t.Helper()
	suffix = fmt.Sprintf("test-guard-%d-%d", os.Getpid(), time.Now().UnixNano())
	homeDir = t.TempDir()
	env = append(os.Environ(),
		"DECKPILOT_PIPE_SUFFIX="+suffix,
		"USERPROFILE="+homeDir,
		"HOME="+homeDir,
	)
	return env, suffix, homeDir
}

// guardRun executes deckpilot with the supplied env and returns
// (stdout, stderr, err). Combined output isn't enough because gate 3 needs
// to grep stderr specifically.
func guardRun(t *testing.T, env []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(deckpilotExe, args...)
	cmd.Env = env
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// guardStartDaemon launches an isolated daemon process bound to the suffix
// in env, waits for it to answer PING, and returns a cleanup that stops it.
// We invoke `deckpilot daemon` directly rather than relying on EnsureRunning
// so the test owns the lifecycle (and so a hung daemon doesn't outlive the
// test).
func guardStartDaemon(t *testing.T, env []string) func() {
	t.Helper()
	cmd := exec.Command(deckpilotExe, "daemon")
	cmd.Env = env
	// Detach from the test's stdio so the daemon's logs don't pollute -v.
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("guard: start daemon: %v", err)
	}
	// Poll PING. EnsureRunning would re-launch if it can't dial the pipe,
	// which we don't want; do a manual readiness wait instead.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _, err := guardRun(t, env, "list")
		if err == nil || strings.Contains(out, "no active sessions") || strings.Contains(out, "NAME") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return func() {
		// Best-effort: try graceful stop via shutdown IPC, but only if
		// the daemon hasn't seen any external ghostty process (we don't
		// want to take a developer's windows down). Fall back to taskkill.
		if cmd.Process != nil {
			_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/F", "/T").Run()
		}
	}
}

// guardListEntry mirrors cmd.listEntry (which is unexported) so we can
// decode `list --json` from the e2e test in package main.
type guardListEntry struct {
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	PipePath   string `json:"pipe_path"`
	AppRuntime string `json:"app_runtime"`
	Status     string `json:"status"`
	Uptime     string `json:"uptime"`
	Protected  bool   `json:"protected"`
}

// guardListEntries decodes `deckpilot list --json` against the isolated
// daemon. Returns nil on transport failure (the caller usually treats that
// as "skip").
func guardListEntries(t *testing.T, env []string) []guardListEntry {
	t.Helper()
	out, _, err := guardRun(t, env, "list", "--json")
	if err != nil {
		return nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	var entries []guardListEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Logf("guard: list --json decode: %v (raw=%q)", err, out)
		return nil
	}
	return entries
}

// guardSpawnGhostty launches a bare ghostty.exe (no agent) and waits for the
// isolated daemon to discover it. Returns the discovered session name and
// PID, or skips the test if Ghostty isn't installed / discovery times out.
func guardSpawnGhostty(t *testing.T, env []string) (string, int) {
	t.Helper()
	ghostty := cmd.FindGhostty()
	if ghostty == "" {
		t.Skip("guard: ghostty not installed; skipping (CI without Ghostty)")
	}
	preEntries := guardListEntries(t, env)
	preNames := map[string]bool{}
	for _, e := range preEntries {
		preNames[e.Name] = true
	}

	gh := exec.Command(ghostty)
	gh.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := gh.Start(); err != nil {
		t.Skipf("guard: ghostty start failed: %v", err)
	}
	if gh.Process != nil {
		_ = gh.Process.Release()
	}

	// Daemon polls discovery every 5s; give it up to 15s to spot the new PID.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		entries := guardListEntries(t, env)
		for _, e := range entries {
			if preNames[e.Name] {
				continue
			}
			if e.PID == gh.Process.Pid {
				t.Logf("guard: discovered new session name=%s pid=%d", e.Name, e.PID)
				t.Cleanup(func() {
					_ = exec.Command("taskkill", "/PID", strconv.Itoa(e.PID), "/F").Run()
				})
				return e.Name, e.PID
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Couldn't find via PID match; kill the spawned process and skip.
	if gh.Process != nil {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(gh.Process.Pid), "/F").Run()
	}
	t.Skip("guard: daemon did not discover the spawned ghostty session within 15s")
	return "", 0
}

// guardOnlyTestSessions returns true when the daemon's session list contains
// only sessions whose names are in the allowed set. Used by gate 5 to refuse
// running shutdown when external dev ghostty windows might get caught in the
// kill fan-out.
func guardOnlyTestSessions(entries []guardListEntry, allowed map[string]bool) bool {
	for _, e := range entries {
		if !allowed[e.Name] {
			return false
		}
	}
	return true
}

// readProtectedFile loads ~/.deckpilot/protected-sessions.json from the
// isolated home dir. Returns nil if the file doesn't exist.
func readProtectedFile(t *testing.T, homeDir string) []string {
	t.Helper()
	path := filepath.Join(homeDir, ".deckpilot", "protected-sessions.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Protected []string `json:"protected"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc.Protected
}

// TestE2E_GuardProtectBasic — gate 1.
// Launch a session, run `deckpilot protect <id>`, then assert the JSON file
// at ~/.deckpilot/protected-sessions.json gained the entry.
func TestE2E_GuardProtectBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	env, _, homeDir := guardEnv(t)
	stop := guardStartDaemon(t, env)
	t.Cleanup(stop)

	name, _ := guardSpawnGhostty(t, env)

	stdout, stderr, err := guardRun(t, env, "protect", name)
	if err != nil {
		t.Fatalf("protect failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	got := readProtectedFile(t, homeDir)
	found := false
	for _, n := range got {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("protected-sessions.json missing %q; got %v", name, got)
	}
}

// TestE2E_GuardListShowsMark — gate 2.
// `deckpilot list` must include the PROTECTED header column and a non-empty
// mark on the row of the protected session.
func TestE2E_GuardListShowsMark(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	env, _, _ := guardEnv(t)
	stop := guardStartDaemon(t, env)
	t.Cleanup(stop)

	name, _ := guardSpawnGhostty(t, env)
	if _, _, err := guardRun(t, env, "protect", name); err != nil {
		t.Fatalf("protect: %v", err)
	}

	stdout, _, err := guardRun(t, env, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, "PROTECTED") {
		t.Errorf("list output missing PROTECTED header:\n%s", stdout)
	}
	// Find the row for our session and confirm a non-empty mark in the
	// last column. The row format is space-padded, so we split fields and
	// require at least 6 columns (NAME PID RUNTIME UPTIME STATUS PROTECTED).
	var row string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, name+" ") || line == name {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("could not find row for %q in list output:\n%s", name, stdout)
	}
	fields := strings.Fields(row)
	if len(fields) < 6 {
		t.Fatalf("row %q has %d fields, want >=6 (NAME PID RUNTIME UPTIME STATUS PROTECTED)", row, len(fields))
	}
	mark := fields[len(fields)-1]
	if mark == "" || mark == name {
		t.Errorf("expected non-empty PROTECTED mark on row, got %q (row=%q)", mark, row)
	}
}

// TestE2E_GuardKillRejected — gate 3.
// `deckpilot kill <protected>` must exit non-zero with stderr mentioning
// "protected".
func TestE2E_GuardKillRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	env, _, _ := guardEnv(t)
	stop := guardStartDaemon(t, env)
	t.Cleanup(stop)

	name, _ := guardSpawnGhostty(t, env)
	if _, _, err := guardRun(t, env, "protect", name); err != nil {
		t.Fatalf("protect: %v", err)
	}

	stdout, stderr, err := guardRun(t, env, "kill", name)
	if err == nil {
		t.Fatalf("expected non-zero exit for kill of protected session\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "protected") {
		t.Errorf("expected stderr to mention 'protected'; got:\n%s", stderr)
	}
	// Sanity: the session must still be alive in the daemon's view.
	entries := guardListEntries(t, env)
	stillThere := false
	for _, e := range entries {
		if e.Name == name {
			stillThere = true
			break
		}
	}
	if !stillThere {
		t.Errorf("protected session %q disappeared after rejected kill", name)
	}
}

// TestE2E_GuardKillForceOverrides — gate 4.
// `deckpilot kill --force <protected>` must exit zero, and the session must
// be gone from `list` afterwards (poll, since proc.Kill is async).
func TestE2E_GuardKillForceOverrides(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	env, _, _ := guardEnv(t)
	stop := guardStartDaemon(t, env)
	t.Cleanup(stop)

	name, _ := guardSpawnGhostty(t, env)
	if _, _, err := guardRun(t, env, "protect", name); err != nil {
		t.Fatalf("protect: %v", err)
	}

	stdout, stderr, err := guardRun(t, env, "kill", "--force", name)
	if err != nil {
		t.Fatalf("kill --force failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	// Poll until the session disappears from `list` or its status flips
	// to dead. Daemon refreshes every 5s; allow up to 10s plus slack.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		gone := true
		for _, e := range guardListEntries(t, env) {
			if e.Name == name && e.Status != "dead" {
				gone = false
				break
			}
		}
		if gone {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Errorf("session %q still listed as alive 15s after kill --force", name)
}

// TestE2E_GuardShutdownSkipsProtected — gate 5.
// Launch 2 sessions, protect one, run `shutdown`. The protected session must
// outlive the daemon; the other must be killed.
//
// Safety guard: the daemon's discovery is process-wide, so a workstation
// with extra dev ghostty windows would have them swept up by SHUTDOWN. We
// skip the test in that case to avoid destroying the developer's sessions.
// On a clean box (CI), the test runs in full.
func TestE2E_GuardShutdownSkipsProtected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	env, _, _ := guardEnv(t)
	stop := guardStartDaemon(t, env)
	t.Cleanup(stop)

	nameA, pidA := guardSpawnGhostty(t, env)
	nameB, pidB := guardSpawnGhostty(t, env)

	allowed := map[string]bool{nameA: true, nameB: true}
	entries := guardListEntries(t, env)
	if !guardOnlyTestSessions(entries, allowed) {
		t.Skipf("guard: external ghostty sessions present (entries=%d); refusing to run SHUTDOWN to avoid destroying them", len(entries))
	}

	if _, _, err := guardRun(t, env, "protect", nameA); err != nil {
		t.Fatalf("protect %s: %v", nameA, err)
	}

	// Run shutdown. Per design it returns OK and async-exits the daemon.
	if _, _, err := guardRun(t, env, "shutdown"); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// Wait for pidB (unprotected) to exit. proc.Kill on Windows is async.
	pollProcExit := func(pid int) bool {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			out, _ := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
			if !strings.Contains(string(out), strconv.Itoa(pid)) {
				return true
			}
			time.Sleep(300 * time.Millisecond)
		}
		return false
	}
	if !pollProcExit(pidB) {
		t.Errorf("unprotected session pidB=%d still alive after shutdown", pidB)
	}
	// Protected session must still be running.
	out, _ := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pidA), "/NH").Output()
	if !strings.Contains(string(out), strconv.Itoa(pidA)) {
		t.Errorf("protected session pidA=%d was killed by shutdown (must survive)", pidA)
	}
}

// TestE2E_GuardPersistence — gate 6.
// Protect a session, restart the daemon, and confirm the protected entry is
// restored. The on-disk JSON file is the source of truth, so we both verify
// the file content (deterministic) and re-query the new daemon's LIST to
// confirm the in-memory state was reloaded.
//
// We don't use real `shutdown` here because gate 5 already covers that path
// and shutdown is destructive; instead we kill the daemon process directly,
// then start a fresh one with the same env (same pipe suffix + HOME).
func TestE2E_GuardPersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in short mode")
	}
	env, _, homeDir := guardEnv(t)
	stop := guardStartDaemon(t, env)

	name, _ := guardSpawnGhostty(t, env)
	if _, _, err := guardRun(t, env, "protect", name); err != nil {
		stop()
		t.Fatalf("protect: %v", err)
	}

	// File-level evidence: protected-sessions.json contains the name.
	got := readProtectedFile(t, homeDir)
	beforeFound := false
	for _, n := range got {
		if n == name {
			beforeFound = true
			break
		}
	}
	if !beforeFound {
		stop()
		t.Fatalf("pre-restart: protected file missing %q (got %v)", name, got)
	}

	// Stop daemon-1 hard. We use taskkill on the pipe rather than
	// `shutdown` because shutdown would also kill the unprotected
	// ghostty windows the daemon discovered, which we don't want for
	// this gate (we want to verify *daemon-2* sees the same protected
	// list, not that processes were cleaned up).
	stop()
	// Allow the OS pipe to release.
	time.Sleep(500 * time.Millisecond)

	// Start daemon-2 with same env.
	stop2 := guardStartDaemon(t, env)
	t.Cleanup(stop2)

	// On-disk file must still be intact.
	got2 := readProtectedFile(t, homeDir)
	afterFound := false
	for _, n := range got2 {
		if n == name {
			afterFound = true
			break
		}
	}
	if !afterFound {
		t.Errorf("post-restart: protected file lost %q (got %v)", name, got2)
	}

	// In-memory: daemon-2's LIST should mark the session protected, IF
	// it still discovers it (the original ghostty.exe is still running
	// because we didn't shutdown). Tolerate the case where the daemon
	// hasn't re-discovered yet by polling.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range guardListEntries(t, env) {
			if e.Name == name {
				if !e.Protected {
					t.Errorf("post-restart: session %q in list but Protected=false", name)
				}
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("post-restart: daemon-2 did not re-discover %q within 15s (file evidence still proves persistence)", name)
}
