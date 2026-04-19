package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// --- matchTail (shared helper used by every adapter) --------------------

func TestMatchTail_MatchesPatternInTail(t *testing.T) {
	content := "line1\nline2\nAction Required now\nline4"
	matched, ok := matchTail(content, []string{"Action Required", "Y/n"}, 15)
	if !ok || matched != "Action Required" {
		t.Errorf("expected match on 'Action Required', got matched=%q ok=%v", matched, ok)
	}
}

func TestMatchTail_IgnoresLinesAboveTail(t *testing.T) {
	// Put the pattern at the top of a 50-line content with tailLines=5.
	// It must not match.
	lines := []string{"Action Required"}
	for i := 0; i < 50; i++ {
		lines = append(lines, "benign line")
	}
	content := strings.Join(lines, "\n")
	_, ok := matchTail(content, []string{"Action Required"}, 5)
	if ok {
		t.Error("expected miss when pattern is outside tail window")
	}
}

func TestMatchTail_EmptyPatternSkipped(t *testing.T) {
	_, ok := matchTail("anything", []string{""}, 10)
	if ok {
		t.Error("empty pattern should never match")
	}
}

func TestMatchTail_NonPositiveTailDefaults(t *testing.T) {
	// tailLines=0 should be treated as the default (15), so a pattern in
	// the last line of a 3-line buffer still matches.
	content := "a\nb\nAction Required"
	_, ok := matchTail(content, []string{"Action Required"}, 0)
	if !ok {
		t.Error("tailLines=0 should fall through to default and find the match")
	}
}

// --- registry (NewApprovalAdapter + newFallbackAdapter) -----------------

func TestNewApprovalAdapter_KnownAgents(t *testing.T) {
	for _, agent := range []string{"claude", "gemini", "codex"} {
		a := NewApprovalAdapter(agent)
		if a == nil {
			t.Fatalf("adapter for %q is nil", agent)
		}
		if a.Name() != agent {
			t.Errorf("Name()=%q want %q", a.Name(), agent)
		}
	}
}

func TestNewApprovalAdapter_UnknownFallsBack(t *testing.T) {
	a := NewApprovalAdapter("totally-unknown-xyz")
	if a.Name() != "fallback" {
		t.Errorf("expected fallback adapter, got %q", a.Name())
	}
}

func TestFallbackAdapter_UnionsAllPatterns(t *testing.T) {
	fb := newFallbackAdapter()
	// Must detect patterns that only appear in each agent's list.
	claudeOnly := "Action Required immediately"  // claude-specific
	geminiOnly := "Do you allow? (y/N) press now" // gemini-specific "(y/N)"
	codexOnly := "Would you like to run the command"

	for _, buf := range []string{claudeOnly, geminiOnly, codexOnly} {
		_, ok := fb.Detect(buf)
		if !ok {
			t.Errorf("fallback missed agent-specific content: %q", buf)
		}
	}
}

// --- per-adapter detection ---------------------------------------------

func TestClaudeAdapter_DetectsClaudePatterns(t *testing.T) {
	a := NewApprovalAdapter("claude")
	m, ok := a.Detect("some noise\nAction Required soon")
	if !ok || m != "Action Required" {
		t.Errorf("claude: matched=%q ok=%v", m, ok)
	}
}

func TestGeminiAdapter_DetectsGeminiPatterns(t *testing.T) {
	a := NewApprovalAdapter("gemini")
	_, ok := a.Detect("blah\n(y/N)?\n")
	if !ok {
		t.Error("gemini: expected (y/N) to match")
	}
}

func TestCodexAdapter_DetectsCodexPatterns(t *testing.T) {
	a := NewApprovalAdapter("codex")
	_, ok := a.Detect("prefix\nPress enter to confirm\nsuffix")
	if !ok {
		t.Error("codex: expected 'Press enter to confirm' to match")
	}
}

func TestAdapter_DoesNotMatchBenignOutput(t *testing.T) {
	// The point of per-agent adapters: Gemini status text uses words
	// like 'Allow'/'Waiting' which must NOT trigger for the Gemini
	// adapter (unlike the claude adapter, which does list 'Allow').
	a := NewApprovalAdapter("gemini")
	_, ok := a.Detect("Allowed files: 42\nWaiting for responses")
	if ok {
		t.Error("gemini adapter should not match benign 'Allow'/'Waiting' text")
	}
}

// --- SendAccept behavior (stub sender) ----------------------------------

func TestPatternsAdapter_SendAcceptCallsSender(t *testing.T) {
	calls := 0
	var gotSession, gotCaller string
	a := &patternsAdapter{
		name:     "test",
		patterns: []string{"x"},
		acceptSender: func(session, caller string) error {
			calls++
			gotSession = session
			gotCaller = caller
			return nil
		},
	}
	if err := a.SendAccept("session-1", "caller-1"); err != nil {
		t.Fatalf("SendAccept: %v", err)
	}
	if calls != 1 || gotSession != "session-1" || gotCaller != "caller-1" {
		t.Errorf("sender invoked calls=%d session=%q caller=%q",
			calls, gotSession, gotCaller)
	}
}

func TestPatternsAdapter_SendAcceptPropagatesError(t *testing.T) {
	wantErr := errors.New("daemon down")
	a := &patternsAdapter{
		name:         "test",
		acceptSender: func(_, _ string) error { return wantErr },
	}
	if err := a.SendAccept("s", "c"); err != wantErr {
		t.Errorf("expected %v, got %v", wantErr, err)
	}
}

// --- ObservationAdapter -------------------------------------------------

func TestObservationAdapter_NameSuffixed(t *testing.T) {
	inner := NewApprovalAdapter("claude")
	obs := &ObservationAdapter{Inner: inner}
	if obs.Name() != "claude-observe" {
		t.Errorf("Name()=%q want claude-observe", obs.Name())
	}
}

func TestObservationAdapter_DetectDelegates(t *testing.T) {
	inner := NewApprovalAdapter("claude")
	obs := &ObservationAdapter{Inner: inner}
	m, ok := obs.Detect("Action Required immediately")
	if !ok || m != "Action Required" {
		t.Errorf("Detect should delegate to inner: matched=%q ok=%v", m, ok)
	}
}

func TestObservationAdapter_SendAcceptSuppressesAndLogs(t *testing.T) {
	realSenderCalls := 0
	inner := &patternsAdapter{
		name: "test",
		acceptSender: func(_, _ string) error {
			realSenderCalls++
			return nil
		},
	}

	var logged string
	obs := &ObservationAdapter{
		Inner: inner,
		Logger: func(format string, args ...any) {
			logged = fmt.Sprintf(format, args...)
		},
	}

	if err := obs.SendAccept("sess", "call"); err != nil {
		t.Fatalf("observation SendAccept returned error: %v", err)
	}
	if realSenderCalls != 0 {
		t.Errorf("inner sender fired %d times, expected 0 (suppressed)", realSenderCalls)
	}
	if !strings.Contains(logged, "sess") || !strings.Contains(logged, "call") {
		t.Errorf("log missing session/caller identifiers: %q", logged)
	}
}

func TestObservationAdapter_NilLoggerIsSafe(t *testing.T) {
	inner := NewApprovalAdapter("gemini")
	obs := &ObservationAdapter{Inner: inner, Logger: nil}
	// Must not panic.
	if err := obs.SendAccept("s", "c"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- ErrObservationOnly is exported ------------------------------------

func TestErrObservationOnly_NonNil(t *testing.T) {
	if ErrObservationOnly == nil {
		t.Error("ErrObservationOnly should be a non-nil sentinel")
	}
	if ErrObservationOnly.Error() == "" {
		t.Error("ErrObservationOnly should have a message")
	}
}

// --- Integration: legacy DetectApprovalPromptForAgent parity ----------

// The adapters must not regress the existing matcher: feeding the same
// input through NewApprovalAdapter(X).Detect and DetectApprovalPromptForAgent(X)
// should yield identical booleans for every registered agent.
func TestAdapterParityWithLegacyMatcher(t *testing.T) {
	samples := []string{
		"Action Required soon",
		"Do you allow? (y/N) press now",
		"Press enter to confirm",
		"completely benign output with no triggers",
		"Allow status: normal",
	}
	for _, agent := range []string{"claude", "gemini", "codex"} {
		adapter := NewApprovalAdapter(agent)
		for _, s := range samples {
			_, legacyOK := DetectApprovalPromptForAgent(s, agent)
			_, newOK := adapter.Detect(s)
			if legacyOK != newOK {
				t.Errorf("parity break for agent=%q sample=%q: legacy=%v adapter=%v",
					agent, s, legacyOK, newOK)
			}
		}
	}
}
