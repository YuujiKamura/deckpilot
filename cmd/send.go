package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// formatETA returns the ETA display line for stderr.
func formatETA(now time.Time, eta time.Duration) string {
	arrival := now.In(cmdJST).Add(eta)
	h := int(eta.Hours())
	m := int(eta.Minutes()) % 60
	var dur string
	if h > 0 {
		dur = fmt.Sprintf("+%dh%dm", h, m)
	} else {
		dur = fmt.Sprintf("+%dm", m)
	}
	return fmt.Sprintf("NOW: %s  |  ETA %s (%s)",
		now.In(cmdJST).Format("2006-01-02 Mon 15:04 MST"),
		arrival.Format("15:04 MST"),
		dur,
	)
}

// Send sends a message+submit via the daemon (which handles pipe resolution,
// watcher pause, drain-sequenced delivery, and caller tracking).
// The message is sent literally — no timestamp injection into the payload.
// Timestamp is recorded in the daemon log, not in the wire payload.
func Send(args []string) {
	var etaDur time.Duration
	hasETA := false

	// Parse flags before positional args
	remaining := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--eta" && i+1 < len(args):
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "send: --eta: invalid duration %q: %v\n", args[i], err)
				os.Exit(1)
			}
			etaDur = d
			hasETA = true
		default:
			remaining = append(remaining, args[i])
		}
	}

	if len(remaining) < 2 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot send [--eta <duration>] <session> <message...>")
		os.Exit(1)
	}
	name := remaining[0]
	// Restore MSYS2-mangled slash commands
	for i := 1; i < len(remaining); i++ {
		remaining[i] = unmangleMSYS(remaining[i])
	}
	message := strings.Join(remaining[1:], " ")

	caller := getCaller()

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	result, err := daemon.DaemonSend(name, message, caller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "%s: %s\n", name, result)

	if hasETA {
		fmt.Fprintln(os.Stderr, formatETA(time.Now(), etaDur))
	}
}
