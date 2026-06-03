package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

type listEntry struct {
	Name               string    `json:"name"`
	PID                int       `json:"pid"`
	PipePath           string    `json:"pipe_path"`
	AppRuntime         string    `json:"app_runtime"`
	Status             string    `json:"status"`
	Uptime             string    `json:"uptime"`
	LastBufferChangeAt time.Time `json:"last_buffer_change_at"`
}

// defaultHangSeconds is the buffer-stall threshold above which LAST_OUTPUT is
// marked with an asterisk to flag a candidate hang. Overridable via
// DECKPILOT_HANG_SECONDS.
const defaultHangSeconds = 480

// hangThresholdSeconds reads the stall threshold from DECKPILOT_HANG_SECONDS,
// falling back to defaultHangSeconds when unset or unparseable. A value <= 0
// disables the asterisk marker entirely.
func hangThresholdSeconds() int {
	if v := os.Getenv("DECKPILOT_HANG_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultHangSeconds
}

// formatLastOutput renders the elapsed time since a session's buffer last
// changed as a compact relative string ("5s" / "2m" / "1h12m"). A zero
// timestamp (no output observed yet) renders as "-". When the elapsed time
// exceeds hangSeconds (and hangSeconds > 0), an asterisk is appended to flag
// the session as a hang candidate.
func formatLastOutput(last, now time.Time, hangSeconds int) string {
	if last.IsZero() {
		return "-"
	}
	elapsed := now.Sub(last)
	if elapsed < 0 {
		elapsed = 0
	}
	secs := int(elapsed.Seconds())

	var s string
	switch {
	case secs < 60:
		s = fmt.Sprintf("%ds", secs)
	case secs < 3600:
		s = fmt.Sprintf("%dm", secs/60)
	default:
		s = fmt.Sprintf("%dh%dm", secs/3600, (secs%3600)/60)
	}

	if hangSeconds > 0 && secs > hangSeconds {
		s += "*"
	}
	return s
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

	if len(sessions) == 0 {
		fmt.Println("no active sessions")
		return
	}

	now := time.Now()
	hangSeconds := hangThresholdSeconds()
	fmt.Printf("%-25s %-6s %-10s %-10s %-10s %s\n", "NAME", "PID", "RUNTIME", "UPTIME", "STATUS", "LAST_OUTPUT")
	for _, s := range sessions {
		fmt.Printf("%-25s %-6d %-10s %-10s %-10s %s\n",
			s.Name, s.PID, s.AppRuntime, s.Uptime, s.Status,
			formatLastOutput(s.LastBufferChangeAt, now, hangSeconds))
	}
}
