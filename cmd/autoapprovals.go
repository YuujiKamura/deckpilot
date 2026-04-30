package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// AutoApprovals monitors a session and automatically sends Enter when an
// approval prompt is detected. Flags: --dry-run, --verbose, --interval <dur>.
// usage: deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose]
func AutoApprovals(args []string) {
	// Parse flags
	var sessionName string
	dryRun := false
	verbose := false
	interval := 2 * time.Second

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--verbose":
			verbose = true
		case "--interval":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "auto-approvals: --interval requires a duration argument (e.g. 2s, 500ms)")
				os.Exit(1)
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "auto-approvals: invalid --interval %q: %v\n", args[i], err)
				os.Exit(1)
			}
			interval = d
		default:
			if strings.HasPrefix(args[i], "--") {
				fmt.Fprintf(os.Stderr, "auto-approvals: unknown flag %q\n", args[i])
				os.Exit(1)
			}
			if sessionName == "" {
				sessionName = args[i]
			}
		}
	}

	if sessionName == "" {
		fmt.Fprintln(os.Stderr, "usage: deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose]")
		os.Exit(1)
	}

	caller := getCaller()

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	sentEnter := false // true after we send Enter; reset when prompt disappears
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	dryTag := ""
	if dryRun {
		dryTag = " [dry-run]"
	}
	fmt.Fprintf(os.Stderr, "auto-approvals%s: monitoring %s (interval=%s, Ctrl+C to stop)\n", dryTag, sessionName, interval)

	for {
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "\nstopped")
			return
		case <-ticker.C:
			content, _, err := daemon.DaemonShow(sessionName, "buffer", caller)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] auto-approvals: %v (retrying...)\n", time.Now().Format("15:04:05"), err)
				continue
			}

			// Skip empty buffer — pipe may not be ready yet
			if strings.TrimSpace(content) == "" {
				continue
			}

			matched, hasPrompt := DetectApprovalPrompt(content)

			// Reset flag once prompt disappears (agent moved on)
			if !hasPrompt {
				sentEnter = false
				continue
			}

			// Auto-approve: send Enter once per prompt appearance
			if !sentEnter {
				ts := time.Now().Format("15:04:05")
				if verbose {
					fmt.Fprintf(os.Stderr, "[%s] Prompt detected (matched: %q)%s\n", ts, matched, dryTag)
				} else {
					fmt.Fprintf(os.Stderr, "[%s] Prompt detected, sending Enter%s\n", ts, dryTag)
				}
				if !dryRun {
					if _, err := daemon.DaemonSend(sessionName, "", caller); err != nil {
						fmt.Fprintf(os.Stderr, "auto-approvals: send failed: %v\n", err)
					}
				}
				sentEnter = true
			}
		}
	}
}
