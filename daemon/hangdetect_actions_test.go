package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------- ActionNotify

func TestActionNotify_LogsToStderr(t *testing.T) {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	oldLogOut := log.Writer()
	log.SetOutput(w)
	defer func() {
		os.Stderr = oldStderr
		log.SetOutput(oldLogOut)
	}()

	info := HangInfo{
		SessionName:  "test-sess",
		RootPID:      1234,
		ProcessCount: 5,
		CPUPercent:   0.5,
		StallSince:   100 * time.Second,
	}
	if err := ActionNotify(nil)(context.Background(), info); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	for _, want := range []string{"[hang]", "pid=1234", "cpu=0.50", "stall=1m40s"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q: %q", want, output)
		}
	}
}

func TestActionNotify_NilDaemonSafe(t *testing.T) {
	info := HangInfo{SessionName: "s"}
	if err := ActionNotify(nil)(context.Background(), info); err != nil {
		t.Errorf("ActionNotify(nil) returned error: %v", err)
	}
}

// ---------------------------------------------------------------- ActionSnapshot

// TestActionSnapshot_WritesHeaderAndMetadata verifies the dump file
// contains the session metadata header even when no pipe is available
// (the CLI should still produce forensically useful evidence).
func TestActionSnapshot_WritesHeaderAndMetadata(t *testing.T) {
	baseDir := t.TempDir()
	info := HangInfo{
		SessionName:  "ghostty-42",
		RootPID:      31415,
		ProcessCount: 3,
		CPUPercent:   0.25,
		StallSince:   75 * time.Second,
		DetectedAt:   time.Date(2026, 4, 19, 14, 30, 5, 0, time.UTC),
	}

	action := ActionSnapshot(nil, baseDir, 50)
	if err := action(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Filename format: <YYYYMMDD-HHMMSS>-<session>.log
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 dump file, got %d", len(entries))
	}
	wantName := "20260419-143005-ghostty-42.log"
	if entries[0].Name() != wantName {
		t.Errorf("filename=%q, want %q", entries[0].Name(), wantName)
	}

	content, err := os.ReadFile(filepath.Join(baseDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(content)
	for _, want := range []string{
		"# deckpilot hang snapshot",
		"# session: ghostty-42",
		"# root_pid: 31415",
		"# process_count: 3",
		"# cpu_percent: 0.25",
		"# stall_since: 1m15s",
		"# detected_at: 2026-04-19T14:30:05Z",
		"# --- buffer tail (50 lines) ---",
		"[snapshot note:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dump missing %q\nfull content:\n%s", want, s)
		}
	}
}

// TestActionSnapshot_DefaultBaseDirFallback verifies that an empty
// baseDir resolves to a writable location without panicking.
func TestActionSnapshot_DefaultBaseDirFallback(t *testing.T) {
	// Override HOME for the duration so we don't pollute the real dir.
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	info := HangInfo{
		SessionName: "default-dir",
		DetectedAt:  time.Now(),
	}
	if err := ActionSnapshot(nil, "", 10)(context.Background(), info); err != nil {
		t.Fatalf("snapshot with empty baseDir failed: %v", err)
	}
}

// TestActionSnapshot_DefaultTailLines verifies that tailLines ≤ 0
// falls back to the 200-line default rather than writing an empty file.
func TestActionSnapshot_DefaultTailLines(t *testing.T) {
	baseDir := t.TempDir()
	info := HangInfo{SessionName: "tail-default", DetectedAt: time.Now()}

	if err := ActionSnapshot(nil, baseDir, 0)(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	entries, _ := os.ReadDir(baseDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	content, _ := os.ReadFile(filepath.Join(baseDir, entries[0].Name()))
	if !strings.Contains(string(content), "buffer tail (200 lines)") {
		t.Errorf("expected default 200-line header, got: %s", string(content))
	}
}

// TestActionSnapshot_SanitizesFilename verifies that session names
// containing path separators or Windows-illegal characters produce a
// safe filename that actually lands on disk.
func TestActionSnapshot_SanitizesFilename(t *testing.T) {
	baseDir := t.TempDir()
	info := HangInfo{
		SessionName: "a/b\\c:d*e?f",
		DetectedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := ActionSnapshot(nil, baseDir, 5)(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	entries, _ := os.ReadDir(baseDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(entries), entries)
	}
	name := entries[0].Name()
	// None of the illegal characters should appear.
	for _, bad := range []string{"/", "\\", ":", "*", "?"} {
		// ":" is OK in the filename's time prefix (e.g. "20260101-000000")?
		// No — our format uses "-" separator, so truly no colon should appear.
		if strings.Contains(name, bad) {
			t.Errorf("sanitized filename still contains %q: %s", bad, name)
		}
	}
}

// TestActionSnapshot_DetectedAtZero_UsesNow verifies that a
// zero-valued DetectedAt does not produce a file named "00010101-*"
// which would sort incorrectly.
func TestActionSnapshot_DetectedAtZero_UsesNow(t *testing.T) {
	baseDir := t.TempDir()
	info := HangInfo{SessionName: "zero-time"}

	before := time.Now().Add(-time.Second)
	if err := ActionSnapshot(nil, baseDir, 10)(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	after := time.Now().Add(time.Second)

	entries, _ := os.ReadDir(baseDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	// Parse the timestamp portion and verify it falls within the call window.
	name := entries[0].Name()
	prefix := strings.Split(name, "-zero-time")[0]
	// The filename uses time.Format("20060102-150405") which emits the
	// local zone's clock reading; parse it back as local so the window
	// comparison below doesn't drift by the UTC offset.
	ts, err := time.ParseInLocation("20060102-150405", prefix, time.Local)
	if err != nil {
		t.Fatalf("parse timestamp from %q: %v", prefix, err)
	}
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("timestamp %v outside call window [%v, %v]", ts, before, after)
	}
}

// ---------------------------------------------------------------- ActionSendCtrlC

func TestActionSendCtrlC_NilDaemon(t *testing.T) {
	err := ActionSendCtrlC(nil)(context.Background(), HangInfo{SessionName: "s"})
	if err == nil || !strings.Contains(err.Error(), "nil daemon") {
		t.Errorf("expected nil-daemon error, got %v", err)
	}
}

// ---------------------------------------------------------------- ChainActions

// TestChainActions_RunsAllInOrder verifies that every non-nil action
// in the chain is invoked, in the declared sequence, even when an
// earlier one errors (non-destructive operations must not be blocked
// by a failing notify).
func TestChainActions_RunsAllInOrder(t *testing.T) {
	var order []string
	a := func(_ context.Context, _ HangInfo) error {
		order = append(order, "a")
		return errors.New("a failed")
	}
	b := func(_ context.Context, _ HangInfo) error {
		order = append(order, "b")
		return nil
	}
	c := func(_ context.Context, _ HangInfo) error {
		order = append(order, "c")
		return errors.New("c failed")
	}
	err := ChainActions(a, b, c)(context.Background(), HangInfo{})
	if err == nil || !strings.Contains(err.Error(), "a failed") {
		t.Errorf("expected first error to surface, got %v", err)
	}
	if got := strings.Join(order, ","); got != "a,b,c" {
		t.Errorf("execution order=%q, want a,b,c", got)
	}
}

// TestChainActions_NilEntriesSkipped verifies that nil action slots
// do not panic — useful for config code that builds the chain
// conditionally.
func TestChainActions_NilEntriesSkipped(t *testing.T) {
	var calls int32
	a := func(_ context.Context, _ HangInfo) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	err := ChainActions(nil, a, nil, a, nil)(context.Background(), HangInfo{})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("non-nil actions called %d times, want 2", got)
	}
}

// TestChainActions_AllSuccessReturnsNil verifies the happy path.
func TestChainActions_AllSuccessReturnsNil(t *testing.T) {
	noop := func(_ context.Context, _ HangInfo) error { return nil }
	if err := ChainActions(noop, noop, noop)(context.Background(), HangInfo{}); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}
