package cmd

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func useTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldHome := deckpilotUserHome
	deckpilotUserHome = func() (string, error) { return dir, nil }
	t.Cleanup(func() { deckpilotUserHome = oldHome })
	return dir
}

func TestReadyMarkerFrom(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   string
	}{
		{
			name:   "python prompt",
			screen: "Python 3.12\n>>> ",
			want:   ">>>",
		},
		{
			name:   "status line with two-space gap",
			screen: "Welcome\n\n? for shortcuts            accept-edits · Gemini 4 Argon · low\n",
			want:   "? for shortcuts",
		},
		{
			name:   "blank screen",
			screen: "   \n\t\n",
			want:   "",
		},
		{
			name:   "shell prompt",
			screen: "some output\nC:\\w>",
			want:   "",
		},
		{
			name:   "one character last line",
			screen: "header\n>",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readyMarkerFrom(tc.screen); got != tc.want {
				t.Fatalf("readyMarkerFrom(%q) = %q, want %q", tc.screen, got, tc.want)
			}
		})
	}
}

func TestSaveAndLoadAgentProfilesRoundTrip(t *testing.T) {
	useTempHome(t)
	oldNow := deckpilotNow
	fixedTime := time.Date(2026, 11, 15, 5, 0, 0, 0, time.UTC)
	deckpilotNow = func() time.Time { return fixedTime }
	t.Cleanup(func() { deckpilotNow = oldNow })

	def := AgentDef{
		Cmd:         "python",
		Args:        []string{"-q"},
		ReadyStr:    ">>>",
		TrustStr:    "trust me",
		SubmitKey:   "\r\n",
		ThinkingStr: "busy",
		ErrorStr:    "Traceback",
		Generic:     true,
	}
	screen := "line1\nline2\nline3\n\nline4\nline5\nline6\n"
	if err := saveAgentProfile("python -q", def, screen); err != nil {
		t.Fatalf("saveAgentProfile: %v", err)
	}

	loaded, err := loadAgentProfiles()
	if err != nil {
		t.Fatalf("loadAgentProfiles: %v", err)
	}
	got, ok := loaded["python -q"]
	if !ok {
		t.Fatalf("expected profile for \"python -q\", got %+v", loaded)
	}
	want := AgentDef{
		Cmd:         "python",
		Args:        []string{"-q"},
		ReadyStr:    ">>>",
		TrustStr:    "trust me",
		SubmitKey:   "\r\n",
		ThinkingStr: "busy",
		ErrorStr:    "Traceback",
		Learned:     true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded profile = %+v, want %+v", got, want)
	}

	raw, err := os.ReadFile(agentProfilesPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var disk map[string]map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	entry := disk["python -q"]
	if entry["learned_at"] != fixedTime.Format(time.RFC3339) {
		t.Fatalf("learned_at = %v, want %v", entry["learned_at"], fixedTime.Format(time.RFC3339))
	}
	wantLearnedFrom := "line2\nline3\nline4\nline5\nline6"
	if entry["learned_from"] != wantLearnedFrom {
		t.Fatalf("learned_from = %q, want %q", entry["learned_from"], wantLearnedFrom)
	}
}

func TestSaveAgentProfileDoesNotOverwriteExisting(t *testing.T) {
	useTempHome(t)

	first := AgentDef{Cmd: "mycli", Args: []string{"--one"}, ReadyStr: "FIRST>"}
	second := AgentDef{Cmd: "mycli", Args: []string{"--two"}, ReadyStr: "SECOND>"}

	if err := saveAgentProfile("mycli", first, "FIRST>"); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := saveAgentProfile("mycli", second, "SECOND>"); err != nil {
		t.Fatalf("second save: %v", err)
	}

	loaded, err := loadAgentProfiles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := loaded["mycli"]; got.ReadyStr != "FIRST>" || len(got.Args) != 1 || got.Args[0] != "--one" {
		t.Fatalf("existing entry was overwritten: %+v", got)
	}
}

func TestResolveAgent(t *testing.T) {
	useTempHome(t)

	// Built-in when there is no file.
	got, err := resolveAgent("claude")
	if err != nil || got.Cmd != "claude" || got.Learned || got.Generic {
		t.Fatalf("expected built-in claude, got %+v err=%v", got, err)
	}

	// Unknown name gives a Generic def.
	got, err = resolveAgent("customcli --fast")
	if err != nil || !got.Generic || got.Learned || got.Cmd != "customcli" {
		t.Fatalf("expected generic customcli, got %+v err=%v", got, err)
	}

	// Empty name returns error.
	if _, err := resolveAgent("   "); err == nil {
		t.Fatal("expected error for blank agent name")
	}

	// Profile entry wins over a built-in name.
	override := AgentDef{Cmd: "claude-custom", Args: []string{"--custom"}, ReadyStr: "READY!"}
	if err := saveAgentProfile("claude", override, "READY!"); err != nil {
		t.Fatalf("saveAgentProfile: %v", err)
	}
	got, err = resolveAgent("claude")
	if err != nil || !got.Learned || got.Cmd != "claude-custom" || got.ReadyStr != "READY!" {
		t.Fatalf("expected learned override for claude, got %+v err=%v", got, err)
	}

	// Broken JSON falls back to built-in and generic.
	if err := os.WriteFile(agentProfilesPath(), []byte("{broken json"), 0o600); err != nil {
		t.Fatalf("WriteFile broken: %v", err)
	}
	got, err = resolveAgent("claude")
	if err != nil || got.Cmd != "claude" || got.Learned || got.Generic {
		t.Fatalf("broken JSON should fall back to built-in claude, got %+v err=%v", got, err)
	}
	got, err = resolveAgent("othercli --flag")
	if err != nil || !got.Generic || got.Cmd != "othercli" {
		t.Fatalf("broken JSON should fall back to generic, got %+v err=%v", got, err)
	}
}

func TestWaitForReadyLearnedFallsBackToSettleWhenMarkerMissing(t *testing.T) {
	agent := AgentDef{
		Cmd:      "somecli",
		ReadyStr: "NEVER_APPEARS",
		Learned:  true,
	}
	screens(t, "C:\\w>cmd /k somecli", "starting...", "somecli v2\nnew prompt: ")
	d, err := waitForReady("s", agent, 2*time.Second)
	if err != nil || !d.ReadyMatched {
		t.Fatalf("learned agent should become ready via settle when marker never appears: err=%v diag=%+v", err, d)
	}
}

func TestWaitForReadyLearnedMatchesMarkerOnFirstSample(t *testing.T) {
	agent := AgentDef{
		Cmd:      "somecli",
		ReadyStr: "READY>>>",
		Learned:  true,
	}
	screens(t, "C:\\w>cmd /k somecli", "booting\nREADY>>> ")
	d, err := waitForReady("s", agent, 2*time.Second)
	if err != nil || !d.ReadyMatched {
		t.Fatalf("learned agent should become ready when marker appears: err=%v diag=%+v", err, d)
	}
	if d.Samples != 2 {
		t.Fatalf("expected ready on sample 2 (first sample containing marker), got %d samples", d.Samples)
	}
}
