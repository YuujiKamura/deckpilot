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
// approval prompt is detected. Flags: --dry-run, --verbose, --interval <dur>,
// --agent <claude|gemini|codex>.
// usage: deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose] [--agent <name>]
func AutoApprovals(args []string) {
	var sessionName string
	dryRun := false
	verbose := false
	interval := 2 * time.Second
	agentType := "" // empty → auto-detect, then fall back to "claude"

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
		case "--agent":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "auto-approvals: --agent requires a name (e.g. claude, gemini, codex)")
				os.Exit(1)
			}
			i++
			agentType = args[i]
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
		fmt.Fprintln(os.Stderr, "usage: deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose] [--agent <name>]")
		os.Exit(1)
	}

	caller := getCaller()

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	// sentEnter: true after we send Enter for the current prompt appearance;
	// reset when the prompt disappears.
	sentEnter := false

	// entersSent counts total Enters sent since last backoff reset.
	// When it reaches maxEntersBeforeBackoff, we pause for backoffDuration.
	entersSent := 0
	const maxEntersBeforeBackoff = 3
	const backoffDuration = 30 * time.Second
	var backoffUntil time.Time

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	dryTag := ""
	if dryRun {
		dryTag = " [dry-run]"
	}
	agentTag := agentType
	if agentTag == "" {
		agentTag = "auto"
	}
	fmt.Fprintf(os.Stderr, "auto-approvals%s: monitoring %s (interval=%s, agent=%s, Ctrl+C to stop)\n",
		dryTag, sessionName, interval, agentTag)

	for {
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "\nstopped")
			return
		case <-ticker.C:
			// Honour backoff period — don't send Enter while cooling down.
			if time.Now().Before(backoffUntil) {
				if verbose {
					fmt.Fprintf(os.Stderr, "[%s] backoff active (until %s)\n",
						time.Now().Format("15:04:05"), backoffUntil.Format("15:04:05"))
				}
				continue
			}

			content, _, err := daemon.DaemonShow(sessionName, "buffer", caller)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] auto-approvals: %v (retrying...)\n", time.Now().Format("15:04:05"), err)
				continue
			}

			if strings.TrimSpace(content) == "" {
				continue
			}

			// Resolve agent type on first non-empty buffer when not specified.
			if agentType == "" {
				if inferred := inferAgentFromBuffer(content); inferred != "" {
					agentType = inferred
					fmt.Fprintf(os.Stderr, "auto-approvals: detected agent=%s\n", agentType)
				}
			}

			// If still not detected, DetectApprovalPromptForAgent will fall back to claude
			// for this check, but we keep agentType as "" for future detection.
			matched, hasPrompt := DetectApprovalPromptForAgent(content, agentType)

			if !hasPrompt {
				sentEnter = false
				entersSent = 0
				continue
			}

			if !sentEnter {
				ts := time.Now().Format("15:04:05")

				// Smart response selection based on agent and pattern.
				// Default is empty string which DaemonSend treats as Enter (\r).
				response := ""
				if agentType == "gemini" {
					switch matched {
					case "Allow execution of", "実行を許可しますか":
						response = "1"
					case "(y/N)":
						response = "y"
					}
				}

				if verbose {
					fmt.Fprintf(os.Stderr, "[%s] Prompt detected (matched: %q, response: %q)%s\n",
						ts, matched, response, dryTag)
				} else {
					respTag := "Enter"
					if response != "" {
						respTag = fmt.Sprintf("%q", response)
					}
					fmt.Fprintf(os.Stderr, "[%s] Prompt detected, sending %s%s\n", ts, respTag, dryTag)
				}

				if !dryRun {
					if _, err := daemon.DaemonSend(sessionName, response, caller); err != nil {
						fmt.Fprintf(os.Stderr, "auto-approvals: send failed: %v\n", err)
					}
				}
				sentEnter = true
				entersSent++

				if entersSent >= maxEntersBeforeBackoff {
					fmt.Fprintf(os.Stderr,
						"[%s] WARNING: sent %d Enter(s) for %q with no resolution; backing off %s\n",
						time.Now().Format("15:04:05"), entersSent, matched, backoffDuration)
					backoffUntil = time.Now().Add(backoffDuration)
					sentEnter = false
					entersSent = 0
				}
			}
		}
	}
}
