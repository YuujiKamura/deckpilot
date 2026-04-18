package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var jst = time.FixedZone("JST", 9*3600)

func fixedNow() time.Time {
	return time.Date(2026, 4, 19, 8, 10, 0, 0, jst)
}

// TestRenderListTable_NowHeader asserts the NOW: header is emitted as the first line.
func TestRenderListTable_NowHeader(t *testing.T) {
	sessions := []listEntry{
		{Name: "ghostty-40328", PID: 40328, AppRuntime: "winui3", Uptime: "5h13m", LastAct: "2m ago", Status: "idle"},
	}
	var buf bytes.Buffer
	renderListTable(sessions, fixedNow(), &buf)
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasPrefix(lines[0], "NOW: 2026-04-19 Sun 08:10 JST") {
		t.Errorf("first line should be NOW header, got %q", lines[0])
	}
}

// TestRenderListTable_LastActColumn asserts LAST-ACT column appears in header row.
func TestRenderListTable_LastActColumn(t *testing.T) {
	sessions := []listEntry{
		{Name: "ghostty-40328", PID: 40328, AppRuntime: "winui3", Uptime: "5h13m", LastAct: "2m ago", Status: "idle"},
	}
	var buf bytes.Buffer
	renderListTable(sessions, fixedNow(), &buf)
	out := buf.String()
	if !strings.Contains(out, "LAST-ACT") {
		t.Errorf("output should contain LAST-ACT column header, got:\n%s", out)
	}
	if !strings.Contains(out, "2m ago") {
		t.Errorf("output should contain last-act value '2m ago', got:\n%s", out)
	}
}

// TestRenderListTable_StaleStatus asserts stale status is shown for sessions idle > 30min.
func TestRenderListTable_StaleStatus(t *testing.T) {
	sessions := []listEntry{
		{Name: "ghostty-36236", PID: 36236, AppRuntime: "winui3", Uptime: "5h16m", LastAct: "4h37m", Status: "stale"},
	}
	var buf bytes.Buffer
	renderListTable(sessions, fixedNow(), &buf)
	out := buf.String()
	if !strings.Contains(out, "stale") {
		t.Errorf("output should contain 'stale' status, got:\n%s", out)
	}
}

// TestRenderListTable_Empty asserts no header line is printed when there are no sessions.
func TestRenderListTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	renderListTable([]listEntry{}, fixedNow(), &buf)
	out := buf.String()
	if strings.Contains(out, "NOW:") {
		t.Errorf("NOW: header should not appear when there are no sessions, got:\n%s", out)
	}
}

// TestFormatLastAct asserts formatLastAct returns human-readable durations.
func TestFormatLastAct(t *testing.T) {
	now := fixedNow()
	cases := []struct {
		changedAt time.Time
		want      string
	}{
		{now.Add(-2 * time.Minute), "2m ago"},
		{now.Add(-90 * time.Second), "1m ago"},
		{now.Add(-30 * time.Second), "30s ago"},
		{now.Add(-4*time.Hour - 37*time.Minute), "4h37m"},
	}
	for _, c := range cases {
		got := formatLastAct(c.changedAt, now)
		if got != c.want {
			t.Errorf("formatLastAct(%v) = %q, want %q", c.changedAt, got, c.want)
		}
	}
}
