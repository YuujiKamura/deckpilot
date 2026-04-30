package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withMetaHome(t *testing.T) string {
	t.Helper()
	tempHome := t.TempDir()
	old := launchMetaUserHome
	t.Cleanup(func() { launchMetaUserHome = old })
	launchMetaUserHome = func() (string, error) { return tempHome, nil }
	return tempHome
}

func TestWriteLaunchMeta_PersistsAndStampsTime(t *testing.T) {
	home := withMetaHome(t)

	oldNow := launchMetaTimeNow
	t.Cleanup(func() { launchMetaTimeNow = oldNow })
	fixed := time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC)
	launchMetaTimeNow = func() time.Time { return fixed }

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
