package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func usePerceptionHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	previous := deckpilotUserHome
	deckpilotUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { deckpilotUserHome = previous })
}

func TestWriteLaunchFailureKeepsFullBufferAndReadinessEvidence(t *testing.T) {
	usePerceptionHome(t)
	diagnostics := readyWaitDiagnostics{
		Samples: 4, ChangedSamples: 2, SawPromptLike: true,
		LastStatus: "active", LastBuffer: "first line\nsecond line\nthird line",
	}
	path, err := writeLaunchFailure("ghostty-123", context.DeadlineExceeded, diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"samples: 4", "changed_samples: 2", "prompt_like_seen: true", "last_status: active", "first line\nsecond line\nthird line"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("failure record missing %q", want)
		}
	}
	if filepath.Dir(path) != launchFailDir() {
		t.Fatalf("failure record path = %q", path)
	}
}

func TestEnrichLaunchMetadata(t *testing.T) {
	usePerceptionHome(t)
	at := time.Date(2026, 11, 8, 12, 30, 0, 0, time.UTC)
	if err := WriteLaunchMeta(LaunchMeta{SessionName: "ghostty-123", Agent: "claude", Cwd: `C:\work`, LaunchedAt: at}); err != nil {
		t.Fatal(err)
	}
	entries := []listEntry{{Name: "ghostty-123"}, {Name: "external"}}
	enrichLaunchMetadata(entries)
	if entries[0].Agent != "claude" || entries[0].Cwd != `C:\work` || entries[0].LaunchedAt != at.Format(time.RFC3339) {
		t.Fatalf("launch metadata not reflected: %+v", entries[0])
	}
	if entries[1].Agent != "" || entries[1].Cwd != "" {
		t.Fatalf("external session unexpectedly got launch metadata: %+v", entries[1])
	}
}

func TestWriteLaunchFailureRecordsUnavailableBuffer(t *testing.T) {
	usePerceptionHome(t)
	diagnostics := readyWaitDiagnostics{LastShowError: "daemon disconnected"}
	path, err := writeLaunchFailure("ghostty-unavailable", context.DeadlineExceeded, diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "last_show_error:") {
		t.Fatalf("show failure missing from launch-fail record: %s", body)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\nd\n", 2); got != "c\nd" {
		t.Fatalf("lastLines = %q", got)
	}
}

func TestQuotePowerShellArgDoesNotBreakOutOfLiteral(t *testing.T) {
	input := "agent'; Write-Host injected"
	want := "'agent''; Write-Host injected'"
	if got := quotePowerShellArg(input); got != want {
		t.Fatalf("quoted arg = %q, want %q", got, want)
	}
}
