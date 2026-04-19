package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
	"golang.org/x/term"
)

var cmdJST = time.FixedZone("JST", 9*3600)

type listEntry struct {
	Name        string `json:"name"`
	PID         int    `json:"pid"`
	PipePath    string `json:"pipe_path"`
	AppRuntime  string `json:"app_runtime"`
	Status      string `json:"status"`
	Uptime      string `json:"uptime"`
	LastAct     string `json:"last_act,omitempty"`
	LastActSecs int64  `json:"last_act_secs,omitempty"`
}

const (
	ansiReset  = "\x1b[0m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
)

// formatLastAct converts a past time into a human-readable "how long ago" string.
// < 1h: "Xm ago" / "Xs ago"; >= 1h: "XhYm" (no "ago").
func formatLastAct(changedAt time.Time, now time.Time) string {
	if changedAt.IsZero() {
		return "unknown"
	}
	d := now.Sub(changedAt).Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm ago", m)
	}
	return fmt.Sprintf("%ds ago", s)
}

// colorLastAct wraps a pre-padded LAST-ACT cell string in ANSI color based on age.
// padded must already be right-padded to the column width so alignment is locked
// before color escapes are added (tabwriter does not strip ANSI bytes).
func colorLastAct(padded string, ageSecs int64, isTTY bool) string {
	if !isTTY || ageSecs <= 0 {
		return padded
	}
	const (
		twoHoursSecs  = 2 * 60 * 60
		thirtyMinSecs = 30 * 60
	)
	switch {
	case ageSecs >= twoHoursSecs:
		return ansiRed + padded + ansiReset
	case ageSecs >= thirtyMinSecs:
		return ansiYellow + padded + ansiReset
	default:
		return padded
	}
}

// renderListTable writes the NOW: header and table to out.
// isTTY controls whether ANSI color is applied to the LAST-ACT column.
// Callers pass time.Now().In(cmdJST) for deterministic testing.
func renderListTable(sessions []listEntry, now time.Time, out io.Writer, isTTY bool) {
	if len(sessions) == 0 {
		fmt.Fprintln(out, "no active sessions")
		return
	}

	fmt.Fprintf(out, "NOW: %s\n\n", now.Format("2006-01-02 Mon 15:04 MST"))
	fmt.Fprintf(out, "%-25s %-6s %-10s %-10s %-11s %s\n", "NAME", "PID", "RUNTIME", "UPTIME", "LAST-ACT", "STATUS")
	for _, s := range sessions {
		lastActCell := colorLastAct(fmt.Sprintf("%-11s", s.LastAct), s.LastActSecs, isTTY)
		fmt.Fprintf(out, "%-25s %-6d %-10s %-10s %s %s\n", s.Name, s.PID, s.AppRuntime, s.Uptime, lastActCell, s.Status)
	}
}

// List prints all active sessions as a table.
func List(args []string) {
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	raw, err := daemon.DaemonList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		os.Exit(1)
	}

	if len(args) > 0 && args[0] == "--json" {
		fmt.Println(raw)
		return
	}

	var sessions []listEntry
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		fmt.Fprintf(os.Stderr, "list: bad json: %v\n", err)
		os.Exit(1)
	}

	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	renderListTable(sessions, time.Now().In(cmdJST), os.Stdout, isTTY)
}
