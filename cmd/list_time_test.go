package cmd

import (
	"bytes"
	"fmt"
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
	renderListTable(sessions, fixedNow(), &buf, false)
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
	renderListTable(sessions, fixedNow(), &buf, false)
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
	renderListTable(sessions, fixedNow(), &buf, false)
	out := buf.String()
	if !strings.Contains(out, "stale") {
		t.Errorf("output should contain 'stale' status, got:\n%s", out)
	}
}

// TestRenderListTable_Empty asserts no header line is printed when there are no sessions.
func TestRenderListTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	renderListTable([]listEntry{}, fixedNow(), &buf, false)
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

// TestColorLastAct_Thresholds verifies color codes are applied based on age.
func TestColorLastAct_Thresholds(t *testing.T) {
	cases := []struct {
		label    string
		ageSecs  int64
		wantCode string // empty = no ANSI
	}{
		{"5m (no color)", 5 * 60, ""},
		{"31m (yellow)", 31 * 60, ansiYellow},
		{"2h05m (red)", (2*60 + 5) * 60, ansiRed},
		{"exactly 30m boundary (yellow)", 30 * 60, ansiYellow},
		{"exactly 2h boundary (red)", 2 * 60 * 60, ansiRed},
	}
	for _, c := range cases {
		padded := fmt.Sprintf("%-11s", "5m ago")
		got := colorLastAct(padded, c.ageSecs, true)
		if c.wantCode == "" {
			if strings.Contains(got, "\x1b[") {
				t.Errorf("%s: expected no ANSI code, got %q", c.label, got)
			}
		} else {
			if !strings.HasPrefix(got, c.wantCode) {
				t.Errorf("%s: expected prefix %q, got %q", c.label, c.wantCode, got)
			}
			if !strings.HasSuffix(got, ansiReset) {
				t.Errorf("%s: expected suffix %q, got %q", c.label, ansiReset, got)
			}
		}
	}
}

// TestColorLastAct_NoTTY verifies no ANSI codes are emitted when isTTY is false (pipe mode).
func TestColorLastAct_NoTTY(t *testing.T) {
	padded := fmt.Sprintf("%-11s", "31m ago")
	got := colorLastAct(padded, 31*60, false)
	if strings.Contains(got, "\x1b[") {
		t.Errorf("pipe mode: expected no ANSI escape sequences, got %q", got)
	}
}

// TestRenderListTable_ColorTTY verifies yellow/red/plain are embedded in table output when TTY.
func TestRenderListTable_ColorTTY(t *testing.T) {
	sessions := []listEntry{
		{Name: "s-fresh", PID: 1, AppRuntime: "winui3", Uptime: "1m", LastAct: "5m ago", LastActSecs: 5 * 60, Status: "idle"},
		{Name: "s-yellow", PID: 2, AppRuntime: "winui3", Uptime: "1h", LastAct: "31m ago", LastActSecs: 31 * 60, Status: "stale"},
		{Name: "s-red", PID: 3, AppRuntime: "winui3", Uptime: "3h", LastAct: "2h05m", LastActSecs: (2*60 + 5) * 60, Status: "stale"},
	}
	var buf bytes.Buffer
	renderListTable(sessions, fixedNow(), &buf, true)
	out := buf.String()

	if !strings.Contains(out, ansiYellow) {
		t.Errorf("TTY output should contain yellow ANSI for 31m session, got:\n%s", out)
	}
	if !strings.Contains(out, ansiRed) {
		t.Errorf("TTY output should contain red ANSI for 2h05m session, got:\n%s", out)
	}

	// s-fresh (5m) row must NOT contain any ANSI code
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "s-fresh") && strings.Contains(line, "\x1b[") {
			t.Errorf("fresh session line should have no ANSI, got %q", line)
		}
	}
}

// TestRenderListTable_NoPipeColor verifies isTTY=false produces no ANSI escapes.
func TestRenderListTable_NoPipeColor(t *testing.T) {
	sessions := []listEntry{
		{Name: "s-stale", PID: 1, AppRuntime: "winui3", Uptime: "3h", LastAct: "2h30m", LastActSecs: (2*60 + 30) * 60, Status: "stale"},
	}
	var buf bytes.Buffer
	renderListTable(sessions, fixedNow(), &buf, false)
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("pipe mode: output must not contain ANSI escapes, got:\n%s", out)
	}
}
