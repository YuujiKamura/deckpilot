package cmd

import (
	"strings"
	"testing"
)

// TestFormatShowHeader asserts the bracketed header shape emitted by show.
func TestFormatShowHeader(t *testing.T) {
	now := fixedNow()
	got := formatShowHeader("ghostty-40328", now, "5h13m", "2m ago", "sonnet", "5h:86% wk:41% sn:9%")
	// Expected: [ghostty-40328 | now 2026-04-19 Sun 08:10 JST | uptime 5h13m | last-act 2m ago]
	if !strings.HasPrefix(got, "[ghostty-40328 | now 2026-04-19 Sun 08:10 JST") {
		t.Errorf("header should start with session name and now timestamp, got %q", got)
	}
	if !strings.Contains(got, "uptime 5h13m") {
		t.Errorf("header should contain uptime, got %q", got)
	}
	if !strings.Contains(got, "last-act 2m ago]") {
		t.Errorf("header should contain last-act, got %q", got)
	}
	if !strings.Contains(got, "model: sonnet") {
		t.Errorf("header should contain model, got %q", got)
	}
	if !strings.Contains(got, "5h:86% wk:41% sn:9%") {
		t.Errorf("header should contain quota, got %q", got)
	}
}

// TestFormatShowHeader_NoLastAct asserts graceful output when last-act is unknown.
func TestFormatShowHeader_NoLastAct(t *testing.T) {
	got := formatShowHeader("ghostty-40328", fixedNow(), "5h13m", "", "", "")
	if !strings.Contains(got, "ghostty-40328") {
		t.Errorf("header should contain session name, got %q", got)
	}
}
