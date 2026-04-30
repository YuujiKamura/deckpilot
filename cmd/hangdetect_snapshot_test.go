package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// snapshotTestRig swaps every package-level seam the snapshot path
// touches and restores them when the test ends. Returns the temp HOME
// the dump will be written under.
type snapshotTestRig struct {
	tempHome string
}

func newSnapshotRig(t *testing.T) *snapshotTestRig {
	t.Helper()
	r := &snapshotTestRig{tempHome: t.TempDir()}

	oldList := hangDetectDaemonListWithContext
	oldTail := hangDetectPipeTailWithContext
	oldHistory := hangDetectPipeHistoryWithContext
	oldHome := deckpilotUserHome
	oldNow := deckpilotNow
	t.Cleanup(func() {
		hangDetectDaemonListWithContext = oldList
		hangDetectPipeTailWithContext = oldTail
		hangDetectPipeHistoryWithContext = oldHistory
		deckpilotUserHome = oldHome
		deckpilotNow = oldNow
	})

	// Single home / clock seam covers hang-dumps, launch-meta, and
	// any future per-session artefact directory.
	deckpilotUserHome = func() (string, error) { return r.tempHome, nil }
	deckpilotNow = func() time.Time {
		return time.Date(2026, 4, 19, 15, 4, 5, 0, time.UTC)
	}
	return r
}

func (r *snapshotTestRig) dumpPath(session string) string {
	return filepath.Join(r.tempHome, ".deckpilot", "hang-dumps",
		fmt.Sprintf("20260419-150405-%s.log", session))
}

// TestSnapshotActionForSession_WritesLiveTailDump pins the original
// v1 contract: the dump file lands at the expected path, contains the
// header, and includes the live tail body. v2 keeps these guarantees.
func TestSnapshotActionForSession_WritesLiveTailDump(t *testing.T) {
	r := newSnapshotRig(t)

	hangDetectDaemonListWithContext = func(context.Context) (string, error) {
		return `[{"name":"ghostty-28900","pid":28900,"pipe_path":"\\\\.\\pipe\\ghostty-28900","app_runtime":"win32","status":"idle","uptime":"1m"}]`, nil
	}
	hangDetectPipeTailWithContext = func(_ context.Context, pipePath string, lines int) (string, error) {
		if pipePath != `\\.\pipe\ghostty-28900` {
			t.Fatalf("pipePath=%q", pipePath)
		}
		if lines != 200 {
			t.Fatalf("lines=%d", lines)
		}
		return "line one\nline two\n", nil
	}
	hangDetectPipeHistoryWithContext = func(_ context.Context, _ string) (string, error) {
		return "", nil
	}

	info := daemon.HangInfo{SessionName: "ghostty-28900", RootPID: 28900, ProcessCount: 1}
	if err := snapshotActionForSession("ghostty-28900")(context.Background(), info); err != nil {
		t.Fatalf("snapshot action: %v", err)
	}

	content, err := os.ReadFile(r.dumpPath("ghostty-28900"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"# deckpilot hang snapshot",
		"# session: ghostty-28900",
		"# --- buffer tail (200 lines) ---",
		"line one",
		"line two",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dump missing %q\n%s", want, text)
		}
	}
}

// TestSnapshotActionForSession_V2RecordsResumeContext is the new
// guarantee: the dump must carry enough metadata for a human or
// another agent to reconstruct what was running and where, so a hung
// session can be resumed in a fresh ghostty without spelunking into
// the daemon. Pins the v2 fields advertised in the resume_hint line.
func TestSnapshotActionForSession_V2RecordsResumeContext(t *testing.T) {
	r := newSnapshotRig(t)

	hangDetectDaemonListWithContext = func(context.Context) (string, error) {
		return `[{
		  "name": "ghostty-7064",
		  "pid": 7064,
		  "pipe_path": "\\\\.\\pipe\\ghostty-winui3-ghostty-7064-7064",
		  "app_runtime": "winui3",
		  "status": "stalled",
		  "uptime": "19m03s"
		}]`, nil
	}
	hangDetectPipeTailWithContext = func(_ context.Context, _ string, _ int) (string, error) {
		// Agent inference falls back to tail when history is empty.
		return "tail context (no banner)\n", nil
	}
	hangDetectPipeHistoryWithContext = func(_ context.Context, _ string) (string, error) {
		// Gemini banner buried far above the tail window.
		return "Welcome to Gemini CLI\n" +
			strings.Repeat("...\n", 500) +
			"gemini> partial answer cut off here\n", nil
	}

	info := daemon.HangInfo{
		SessionName:  "ghostty-7064",
		RootPID:      7064,
		ProcessCount: 3,
		CPUPercent:   0.5,
		StallSince:   60 * time.Second,
	}
	if err := snapshotActionForSession("ghostty-7064")(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	content, err := os.ReadFile(r.dumpPath("ghostty-7064"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"# deckpilot hang snapshot v2",
		"# inferred_agent: gemini",
		"# app_runtime: winui3",
		"# uptime: 19m03s",
		"# status: stalled",
		"# pipe_path: \\\\.\\pipe\\ghostty-winui3-ghostty-7064-7064",
		"# resume_hint:",
		"# --- buffer tail (200 lines) ---",
		"tail context (no banner)",
		"# --- full history (HISTORY|0) ---",
		"Welcome to Gemini CLI",
		"gemini> partial answer cut off here",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dump missing %q\nfull dump:\n%s", want, text)
		}
	}
}

// TestSnapshotActionForSession_V2EmitsResumeCommandFromLaunchMeta is
// the stage-2 contract: when a session was started via `deckpilot
// launch`, snapshot v2 must emit an exact `# resume_command:` line so
// an operator can copy-paste it into a fresh shell and respawn the
// agent in the right cwd with the original prompt — no buffer
// spelunking required.
func TestSnapshotActionForSession_V2EmitsResumeCommandFromLaunchMeta(t *testing.T) {
	r := newSnapshotRig(t)

	// Pre-populate launch-meta as if `deckpilot launch gemini ...`
	// had recorded it before the hang.
	if err := WriteLaunchMeta(LaunchMeta{
		SessionName: "ghostty-7064",
		Agent:       "gemini",
		Cwd:         `C:\Users\yuuji\ghostty-win`,
		Prompt:      "fix issue #42 with the new approach",
		LaunchedAt:  time.Date(2026, 4, 26, 17, 30, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	hangDetectDaemonListWithContext = func(context.Context) (string, error) {
		return `[{
		  "name": "ghostty-7064",
		  "pid": 7064,
		  "pipe_path": "\\\\.\\pipe\\ghostty-winui3-ghostty-7064-7064",
		  "app_runtime": "winui3",
		  "status": "stalled",
		  "uptime": "19m03s"
		}]`, nil
	}
	hangDetectPipeTailWithContext = func(context.Context, string, int) (string, error) {
		return "tail body\n", nil
	}
	hangDetectPipeHistoryWithContext = func(context.Context, string) (string, error) {
		return "Welcome to Gemini CLI\nworking on issue 42\n", nil
	}

	info := daemon.HangInfo{SessionName: "ghostty-7064", RootPID: 7064}
	if err := snapshotActionForSession("ghostty-7064")(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	body, err := os.ReadFile(r.dumpPath("ghostty-7064"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"# launched_agent: gemini",
		`# launched_cwd: C:\Users\yuuji\ghostty-win`,
		"# launched_at: 2026-04-26T17:30:00Z",
		"# original_prompt: fix issue #42 with the new approach",
		// resume_command must quote the cwd (backslash) and the
		// multi-word prompt so a copy-paste survives the shell.
		`# resume_command: deckpilot launch gemini "fix issue #42 with the new approach" --cwd "C:\Users\yuuji\ghostty-win"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dump missing %q\nfull dump:\n%s", want, text)
		}
	}
	// Without launch-meta, snapshot prints a "launched_by: external"
	// line. With launch-meta, that line must NOT appear.
	if strings.Contains(text, "launched_by: external") {
		t.Errorf("external marker leaked even though launch-meta was present:\n%s", text)
	}
}

// TestSnapshotActionForSession_V2NoLaunchMetaShowsExternalMarker
// pins the inverse: a session opened manually (no meta file) gets the
// "external" marker plus the generic resume_hint.
func TestSnapshotActionForSession_V2NoLaunchMetaShowsExternalMarker(t *testing.T) {
	r := newSnapshotRig(t)

	hangDetectDaemonListWithContext = func(context.Context) (string, error) {
		return `[{"name":"ghostty-99","pid":99,"pipe_path":"p","app_runtime":"winui3","status":"idle","uptime":"5s"}]`, nil
	}
	hangDetectPipeTailWithContext = func(context.Context, string, int) (string, error) {
		return "", nil
	}
	hangDetectPipeHistoryWithContext = func(context.Context, string) (string, error) {
		return "", nil
	}

	info := daemon.HangInfo{SessionName: "ghostty-99", RootPID: 99}
	if err := snapshotActionForSession("ghostty-99")(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	text, _ := os.ReadFile(r.dumpPath("ghostty-99"))
	if !strings.Contains(string(text), "launched_by: external") {
		t.Errorf("expected external marker; got:\n%s", text)
	}
	if strings.Contains(string(text), "resume_command:") {
		t.Errorf("must not synthesize a resume_command without launch-meta:\n%s", text)
	}
}

// TestSnapshotActionForSession_HistoryFailureStillWritesTail covers
// the degraded path where pipe.History() returns an error (not every
// runtime supports HISTORY). The dump must still land — losing the
// scrollback is acceptable, losing the whole snapshot is not.
func TestSnapshotActionForSession_HistoryFailureStillWritesTail(t *testing.T) {
	r := newSnapshotRig(t)

	hangDetectDaemonListWithContext = func(context.Context) (string, error) {
		return `[{"name":"ghostty-1","pid":1,"pipe_path":"p","app_runtime":"win32","status":"idle","uptime":"1m"}]`, nil
	}
	hangDetectPipeTailWithContext = func(_ context.Context, _ string, _ int) (string, error) {
		return "tail body\n", nil
	}
	hangDetectPipeHistoryWithContext = func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("HISTORY|0: server: PARSE_ERROR")
	}

	info := daemon.HangInfo{SessionName: "ghostty-1", RootPID: 1}
	if err := snapshotActionForSession("ghostty-1")(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	text, err := os.ReadFile(r.dumpPath("ghostty-1"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	s := string(text)
	if !strings.Contains(s, "tail body") {
		t.Errorf("tail must survive history failure; got:\n%s", s)
	}
	if !strings.Contains(s, "[history unavailable: HISTORY|0: server: PARSE_ERROR]") {
		t.Errorf("history failure must be surfaced as an in-dump note; got:\n%s", s)
	}
	if !strings.Contains(s, "# inferred_agent: unknown") {
		t.Errorf("unknown agent must be written explicitly (no silent claude default); got:\n%s", s)
	}
}

// TestSnapshotActionForSession_MissingSessionStillDumpsHeader covers
// the worst case: the hang has already torn the session out of LIST
// by the time the action runs. The metadata block must say so and the
// dump file must still be written so the operator has a timestamp /
// pid / cpu trace to start from.
func TestSnapshotActionForSession_MissingSessionStillDumpsHeader(t *testing.T) {
	r := newSnapshotRig(t)

	hangDetectDaemonListWithContext = func(context.Context) (string, error) {
		return `[]`, nil
	}
	hangDetectPipeTailWithContext = func(_ context.Context, _ string, _ int) (string, error) {
		t.Fatal("tail should not be called when session is missing from LIST")
		return "", nil
	}
	hangDetectPipeHistoryWithContext = func(_ context.Context, _ string) (string, error) {
		t.Fatal("history should not be called when session is missing from LIST")
		return "", nil
	}

	info := daemon.HangInfo{SessionName: "ghostty-gone", RootPID: 9999}
	if err := snapshotActionForSession("ghostty-gone")(context.Background(), info); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	text, err := os.ReadFile(r.dumpPath("ghostty-gone"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	s := string(text)
	if !strings.Contains(s, "# session_metadata_error: session not found: ghostty-gone") {
		t.Errorf("missing session must be recorded explicitly; got:\n%s", s)
	}
	if !strings.Contains(s, "[snapshot note:") {
		t.Errorf("expected fallback snapshot note in body; got:\n%s", s)
	}
}
