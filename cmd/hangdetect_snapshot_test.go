package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

func TestSnapshotActionForSession_WritesLiveTailDump(t *testing.T) {
	tempHome := t.TempDir()

	oldList := hangDetectDaemonListWithContext
	oldTail := hangDetectPipeTailWithContext
	oldHome := hangDetectUserHome
	oldNow := hangDetectNow
	t.Cleanup(func() {
		hangDetectDaemonListWithContext = oldList
		hangDetectPipeTailWithContext = oldTail
		hangDetectUserHome = oldHome
		hangDetectNow = oldNow
	})

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
	hangDetectUserHome = func() (string, error) { return tempHome, nil }
	hangDetectNow = func() time.Time {
		return time.Date(2026, 4, 19, 15, 4, 5, 0, time.UTC)
	}

	info := daemon.HangInfo{
		SessionName:  "ghostty-28900",
		RootPID:      28900,
		ProcessCount: 1,
		CPUPercent:   0,
		StallSince:   0,
	}

	action := snapshotActionForSession("ghostty-28900")
	if err := action(context.Background(), info); err != nil {
		t.Fatalf("snapshot action: %v", err)
	}

	dumpPath := filepath.Join(tempHome, ".deckpilot", "hang-dumps", "20260419-150405-ghostty-28900.log")
	content, err := os.ReadFile(dumpPath)
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
