package cmd

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

func TestParseGitStatusPorcelain(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		got := parseGitStatusPorcelain("")
		if len(got) != 0 {
			t.Fatalf("expected empty slice, got %v", got)
		}
	})

	t.Run("modified, untracked, rename, quoted space, local.md skipped", func(t *testing.T) {
		in := " M cmd/status.go\n" +
			"?? cmd/status_test.go\n" +
			"R  old/path.go -> new/path.go\n" +
			"M  \"dir with space/file.go\"\n" +
			"?? BRIEF.local.md\n" +
			" M notes/todo.local.md\n"
		want := []string{
			"cmd/status.go",
			"cmd/status_test.go",
			"new/path.go",
			"dir with space/file.go",
		}
		got := parseGitStatusPorcelain(in)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestInterestingChildren(t *testing.T) {
	t.Run("tree with only root gives empty slice", func(t *testing.T) {
		tree := []daemon.ProcessInfo{
			{PID: 100, PPID: 1, Name: "ghostty.exe"},
		}
		got := interestingChildren(tree, "agy")
		if len(got) != 0 {
			t.Fatalf("expected empty slice, got %v", got)
		}
	})

	t.Run("root dropped, shells dropped, agent exe dropped, duplicates collapsed, sorted", func(t *testing.T) {
		tree := []daemon.ProcessInfo{
			{PID: 100, PPID: 1, Name: "root-custom.exe"},
			{PID: 101, PPID: 100, Name: "conhost.exe"},
			{PID: 102, PPID: 100, Name: "CMD.EXE"},
			{PID: 103, PPID: 102, Name: "PowerShell.exe"},
			{PID: 104, PPID: 103, Name: "pwsh.exe"},
			{PID: 105, PPID: 103, Name: "bash.exe"},
			{PID: 106, PPID: 105, Name: "sh.exe"},
			{PID: 107, PPID: 103, Name: "node.exe"},
			{PID: 108, PPID: 100, Name: "OpenConsole.exe"},
			{PID: 109, PPID: 100, Name: "ghostty.exe"},
			{PID: 110, PPID: 102, Name: "AGY.exe"},
			{PID: 111, PPID: 110, Name: "agy"},
			{PID: 112, PPID: 110, Name: "git.exe"},
			{PID: 113, PPID: 110, Name: "go.exe"},
			{PID: 114, PPID: 113, Name: "go.exe"},
		}
		got := interestingChildren(tree, "agy.exe")
		want := []string{"git.exe", "go.exe"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}

		got2 := interestingChildren(tree, "AGY")
		if !reflect.DeepEqual(got2, want) {
			t.Fatalf("got %v, want %v", got2, want)
		}
	})
}

func TestDecideState(t *testing.T) {
	tests := []struct {
		name         string
		listStatus   string
		screenWaits  string
		children     []string
		latestAgeSec int
		want         string
	}{
		{
			name:         "rule: dead",
			listStatus:   "dead",
			screenWaits:  "",
			children:     nil,
			latestAgeSec: -1,
			want:         "dead",
		},
		{
			name:         "rule: waiting-for-answer",
			listStatus:   "active",
			screenWaits:  "menu",
			children:     nil,
			latestAgeSec: -1,
			want:         "waiting-for-answer",
		},
		{
			name:         "rule: running-command",
			listStatus:   "active",
			screenWaits:  "",
			children:     []string{"go.exe"},
			latestAgeSec: -1,
			want:         "running-command",
		},
		{
			name:         "rule: editing",
			listStatus:   "idle",
			screenWaits:  "",
			children:     nil,
			latestAgeSec: 45,
			want:         "editing",
		},
		{
			name:         "rule: quiet when age >= 120",
			listStatus:   "idle",
			screenWaits:  "",
			children:     nil,
			latestAgeSec: 120,
			want:         "quiet",
		},
		{
			name:         "rule: quiet when no changed file",
			listStatus:   "idle",
			screenWaits:  "",
			children:     nil,
			latestAgeSec: -1,
			want:         "quiet",
		},
		{
			name:         "ordering: dead beats everything",
			listStatus:   "dead",
			screenWaits:  "trust",
			children:     []string{"go.exe"},
			latestAgeSec: 10,
			want:         "dead",
		},
		{
			name:         "ordering: waiting beats running",
			listStatus:   "active",
			screenWaits:  "trust",
			children:     []string{"go.exe"},
			latestAgeSec: 10,
			want:         "waiting-for-answer",
		},
		{
			name:         "ordering: running beats editing",
			listStatus:   "active",
			screenWaits:  "",
			children:     []string{"git.exe"},
			latestAgeSec: 5,
			want:         "running-command",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decideState(tc.listStatus, tc.screenWaits, tc.children, tc.latestAgeSec)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatStatusLine(t *testing.T) {
	t.Run("full line with every field", func(t *testing.T) {
		s := workerStatus{
			Session:      "ghostty-1234",
			State:        "waiting-for-answer",
			SinceLaunch:  7*time.Minute + 30*time.Second,
			ChangedFiles: 3,
			LatestFile:   "cmd/status.go",
			LatestAgeSec: 12,
			Commits:      2,
			Children:     []string{"git.exe", "go.exe"},
			ScreenWaits:  "menu",
			Notes:        []string{"screen unreadable", "git rev-list failed"},
		}
		want := "ghostty-1234 | waiting-for-answer | screen: menu | files 3 (latest cmd/status.go 12s ago) | commits 2 | running git.exe,go.exe | launched 7m ago | note: screen unreadable; git rev-list failed"
		if got := formatStatusLine(s); got != want {
			t.Fatalf("got:\n  %q\nwant:\n  %q", got, want)
		}
	})

	t.Run("no changed files", func(t *testing.T) {
		s := workerStatus{
			Session:      "ghostty-1234",
			State:        "running-command",
			SinceLaunch:  3 * time.Minute,
			ChangedFiles: 0,
			LatestAgeSec: -1,
			Commits:      1,
			Children:     []string{"go.exe"},
		}
		want := "ghostty-1234 | running-command | files 0 | commits 1 | running go.exe | launched 3m ago"
		if got := formatStatusLine(s); got != want {
			t.Fatalf("got:\n  %q\nwant:\n  %q", got, want)
		}
	})

	t.Run("no children", func(t *testing.T) {
		s := workerStatus{
			Session:      "ghostty-5678",
			State:        "editing",
			SinceLaunch:  15 * time.Minute,
			ChangedFiles: 1,
			LatestFile:   "main.go",
			LatestAgeSec: 42,
			Commits:      0,
		}
		want := "ghostty-5678 | editing | files 1 (latest main.go 42s ago) | commits 0 | launched 15m ago"
		if got := formatStatusLine(s); got != want {
			t.Fatalf("got:\n  %q\nwant:\n  %q", got, want)
		}
	})

	t.Run("with screen wait", func(t *testing.T) {
		s := workerStatus{
			Session:      "ghostty-9000",
			State:        "waiting-for-answer",
			SinceLaunch:  0,
			ChangedFiles: 0,
			LatestAgeSec: -1,
			Commits:      0,
			ScreenWaits:  "trust",
		}
		want := "ghostty-9000 | waiting-for-answer | screen: trust | files 0 | commits 0 | launched 0m ago"
		if got := formatStatusLine(s); got != want {
			t.Fatalf("got:\n  %q\nwant:\n  %q", got, want)
		}
	})

	t.Run("with notes", func(t *testing.T) {
		s := workerStatus{
			Session:      "ghostty-9999",
			State:        "quiet",
			SinceLaunch:  2 * time.Minute,
			ChangedFiles: 0,
			LatestAgeSec: -1,
			Commits:      0,
			Notes:        []string{"cwd is not a git repository"},
		}
		want := "ghostty-9999 | quiet | files 0 | commits 0 | launched 2m ago | note: cwd is not a git repository"
		if got := formatStatusLine(s); got != want {
			t.Fatalf("got:\n  %q\nwant:\n  %q", got, want)
		}
	})
}

func TestDropHelpersKeepsOnlyLateChildren(t *testing.T) {
	launch := time.Date(2026, 11, 15, 5, 0, 0, 0, time.UTC)
	cutoff := launch.Add(helperStartWindow)
	starts := map[uint32]time.Time{
		2: launch.Add(10 * time.Second), // MCP server, started with the agent
		3: launch.Add(10 * time.Minute), // go test, started by the worker
		4: launch.Add(11 * time.Minute), // this status process
	}
	started := func(pid uint32) (time.Time, bool) { t, ok := starts[pid]; return t, ok }
	tree := []daemon.ProcessInfo{
		{PID: 1, Name: "ghostty.exe"},
		{PID: 2, PPID: 1, Name: "python.exe"},
		{PID: 3, PPID: 1, Name: "go.exe"},
		{PID: 4, PPID: 1, Name: "deckpilot.exe"},
		{PID: 5, PPID: 1, Name: "unknown-start.exe"},
	}
	got := dropHelpers(tree, started, cutoff, 4)
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
	}
	want := "ghostty.exe,go.exe,unknown-start.exe"
	if strings.Join(names, ",") != want {
		t.Fatalf("got %v, want %s", names, want)
	}
}
