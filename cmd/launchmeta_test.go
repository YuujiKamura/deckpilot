package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func withMetaHome(t *testing.T) string {
	t.Helper()
	tempHome := t.TempDir()
	old := deckpilotUserHome
	t.Cleanup(func() { deckpilotUserHome = old })
	deckpilotUserHome = func() (string, error) { return tempHome, nil }
	return tempHome
}

func TestWriteLaunchMeta_PersistsAndStampsTime(t *testing.T) {
	home := withMetaHome(t)

	oldNow := deckpilotNow
	t.Cleanup(func() { deckpilotNow = oldNow })
	fixed := time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC)
	deckpilotNow = func() time.Time { return fixed }

	meta := LaunchMeta{
		SessionName: "ghostty-7064",
		Agent:       "gemini",
		Cwd:         `C:\Users\yuuji\ghostty-win`,
		Prompt:      "fix issue #42",
	}
	if err := WriteLaunchMeta(meta); err != nil {
		t.Fatalf("write: %v", err)
	}

	path := filepath.Join(home, ".deckpilot", "launch-meta", "ghostty-7064.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, want := range []string{
		`"session_name": "ghostty-7064"`,
		`"agent": "gemini"`,
		`"prompt": "fix issue #42"`,
		`"launched_at": "2026-04-26T18:00:00Z"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestReadLaunchMeta_RoundTrip(t *testing.T) {
	withMetaHome(t)

	meta := LaunchMeta{
		SessionName: "ghostty-1",
		Agent:       "claude",
		Cwd:         `/tmp/work`,
		Prompt:      "hello",
		LaunchedAt:  time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
	}
	if err := WriteLaunchMeta(meta); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok, err := ReadLaunchMeta("ghostty-1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Agent != "claude" || got.Cwd != "/tmp/work" || got.Prompt != "hello" {
		t.Errorf("round-trip lost data: %+v", got)
	}
	if !got.LaunchedAt.Equal(meta.LaunchedAt) {
		t.Errorf("LaunchedAt round-trip: got %v, want %v", got.LaunchedAt, meta.LaunchedAt)
	}
}

func TestReadLaunchMeta_MissingIsNotError(t *testing.T) {
	withMetaHome(t)

	got, ok, err := ReadLaunchMeta("ghostty-never-launched-by-deckpilot")
	if err != nil {
		t.Errorf("missing file should be silent, got err=%v", err)
	}
	if ok {
		t.Errorf("expected ok=false for missing file, got %+v", got)
	}
}

func TestReadLaunchMeta_ParseErrorPropagates(t *testing.T) {
	home := withMetaHome(t)
	dir := filepath.Join(home, ".deckpilot", "launch-meta")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "ghostty-broken.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, ok, err := ReadLaunchMeta("ghostty-broken")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if ok {
		t.Error("ok must be false on parse error")
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `""`},
		{"simple", "simple"},
		{"has space", `"has space"`},
		{`with"quote`, `"with\"quote"`},
		{`C:\Users\yuuji`, `"C:\Users\yuuji"`}, // backslash triggers quoting
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

// TestWriteLaunchMeta_FileMode pins issue #30 stage-1: every
// launch-meta JSON is created with 0o600 so secrets pasted into the
// prompt cannot be read by other accounts on shared POSIX hosts. On
// Windows the OS does not derive ACLs from the Unix bits, so the
// assertion is the intent (mode argument actually passed to
// os.WriteFile) — we verify it on POSIX and just smoke-test on
// Windows that the file is at least owner-readable.
func TestWriteLaunchMeta_FileMode(t *testing.T) {
	home := withMetaHome(t)
	if err := WriteLaunchMeta(LaunchMeta{
		SessionName: "ghostty-mode",
		Agent:       "claude",
		Cwd:         "/tmp",
		Prompt:      "secret-token-abc",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := filepath.Join(home, ".deckpilot", "launch-meta", "ghostty-mode.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if runtime.GOOS == "windows" {
		// NTFS doesn't honor the Unix mode bits — Go reports
		// 0o666 for owner-writable files. Just confirm the file
		// is readable; the 0o600 intent in WriteLaunchMeta is
		// what protects POSIX hosts and documents the policy.
		if perm&0o400 == 0 {
			t.Errorf("file should be owner-readable on Windows; got %o", perm)
		}
	} else {
		if perm != 0o600 {
			t.Errorf("expected mode 0o600, got %o", perm)
		}
	}
}

// TestWriteLaunchMeta_RedactedScrubsPrompt is issue #30 stage-2: when
// Redacted=true the persisted JSON must NOT contain the prompt verbatim,
// even though the in-memory LaunchMeta still carries it. Re-reading the
// file should yield an empty Prompt and Redacted=true so snapshot v2
// can branch on it.
func TestWriteLaunchMeta_RedactedScrubsPrompt(t *testing.T) {
	home := withMetaHome(t)
	secret := "sk-do-not-leak-this-token-1234567890"
	if err := WriteLaunchMeta(LaunchMeta{
		SessionName: "ghostty-redacted",
		Agent:       "claude",
		Cwd:         "/work",
		Prompt:      secret,
		Redacted:    true,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := filepath.Join(home, ".deckpilot", "launch-meta", "ghostty-redacted.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("redacted JSON leaked the prompt:\n%s", body)
	}
	if !strings.Contains(string(body), `"redacted": true`) {
		t.Errorf("expected redacted flag in JSON; got:\n%s", body)
	}
	got, ok, err := ReadLaunchMeta("ghostty-redacted")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if got.Prompt != "" {
		t.Errorf("Prompt must be empty after redacted round-trip, got %q", got.Prompt)
	}
	if !got.Redacted {
		t.Errorf("Redacted flag lost on round-trip")
	}
}

// TestParseLaunchArgs covers the args parser carved out of Launch:
// agent + prompt collection, --cwd capture, --no-meta-prompt flag
// detection, and the missing-prompt error. ParseLaunchArgs is the
// only path Launch uses to interpret argv, so pinning it here keeps
// the redaction flag from regressing into a string token in the
// prompt body.
func TestParseLaunchArgs(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		defaultCwd    string
		wantAgent     string
		wantPrompt    string
		wantCwd       string
		wantNoMeta    bool
		wantSharedCwd bool
		wantErrSubstr string
	}{
		{
			name:       "basic",
			args:       []string{"claude", "hello", "world"},
			defaultCwd: "/d",
			wantAgent:  "claude",
			wantPrompt: "hello world",
			wantCwd:    "/d",
		},
		{
			name:       "with cwd",
			args:       []string{"gemini", "fix", "bug", "--cwd", "/work"},
			defaultCwd: "/d",
			wantAgent:  "gemini",
			wantPrompt: "fix bug",
			wantCwd:    "/work",
		},
		{
			name:       "no-meta-prompt before prompt",
			args:       []string{"claude", "--no-meta-prompt", "secret-prompt"},
			defaultCwd: "/d",
			wantAgent:  "claude",
			wantPrompt: "secret-prompt",
			wantCwd:    "/d",
			wantNoMeta: true,
		},
		{
			name:       "no-meta-prompt mixed with cwd",
			args:       []string{"claude", "do", "thing", "--no-meta-prompt", "--cwd", "/w"},
			defaultCwd: "/d",
			wantAgent:  "claude",
			wantPrompt: "do thing",
			wantCwd:    "/w",
			wantNoMeta: true,
		},
		{
			name:          "shared cwd opt out",
			args:          []string{"claude", "do", "thing", "--shared-cwd"},
			defaultCwd:    "/d",
			wantAgent:     "claude",
			wantPrompt:    "do thing",
			wantCwd:       "/d",
			wantSharedCwd: true,
		},
		{
			name:          "missing prompt",
			args:          []string{"claude"},
			defaultCwd:    "/d",
			wantErrSubstr: "usage:",
		},
		{
			name:          "cwd without value",
			args:          []string{"claude", "hi", "--cwd"},
			defaultCwd:    "/d",
			wantErrSubstr: "--cwd requires",
		},
		{
			name:          "no args",
			args:          nil,
			defaultCwd:    "/d",
			wantErrSubstr: "usage:",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, errMsg := ParseLaunchArgs(c.args, c.defaultCwd)
			if c.wantErrSubstr != "" {
				if !strings.Contains(errMsg, c.wantErrSubstr) {
					t.Fatalf("err=%q, want substring %q", errMsg, c.wantErrSubstr)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected err: %s", errMsg)
			}
			if got.AgentName != c.wantAgent {
				t.Errorf("AgentName=%q want %q", got.AgentName, c.wantAgent)
			}
			if got.Prompt != c.wantPrompt {
				t.Errorf("Prompt=%q want %q", got.Prompt, c.wantPrompt)
			}
			if got.Cwd != c.wantCwd {
				t.Errorf("Cwd=%q want %q", got.Cwd, c.wantCwd)
			}
			if got.NoMetaPrompt != c.wantNoMeta {
				t.Errorf("NoMetaPrompt=%v want %v", got.NoMetaPrompt, c.wantNoMeta)
			}
			if got.SharedCwd != c.wantSharedCwd {
				t.Errorf("SharedCwd=%v want %v", got.SharedCwd, c.wantSharedCwd)
			}
		})
	}
}

func TestOneLineForHeader_CollapsesAndTruncates(t *testing.T) {
	in := "line one\nline two\n\n\tline three   trailing  "
	got := oneLineForHeader(in)
	want := "line one line two line three trailing"
	if got != want {
		t.Errorf("collapse: got %q, want %q", got, want)
	}

	long := strings.Repeat("x", 250)
	got = oneLineForHeader(long)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected truncation suffix, got len=%d", len(got))
	}
	if len(got) != 203 { // 200 + "..."
		t.Errorf("expected length 203, got %d", len(got))
	}
}
