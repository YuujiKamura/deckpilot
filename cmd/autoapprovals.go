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
// --agent <claude|gemini|codex>, --observe (detect-only, never send).
// usage: deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose] [--agent <name>] [--observe]
//
// Phase 2 (issue #25): --observe wraps the selected agent adapter in
// ObservationAdapter so matches are logged without any keystroke injection.
// Use this to discover new approval patterns in production safely.
func AutoApprovals(args []string) {
	var sessionName string
	dryRun := false
	verbose := false
	observeMode := false
	interval := 2 * time.Second
	agentType := "" // empty → auto-detect, then fall back to "claude"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--verbose":
			verbose = true
		case "--observe":
			observeMode = true
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
				} else {
					agentType = "claude"
					fmt.Fprintf(os.Stderr, "auto-approvals: agent not detected, defaulting to claude\n")
				}
			}

			matched, hasPrompt := DetectApprovalPromptForAgent(content, agentType)

			if !hasPrompt {
				sentEnter = false
				continue
			}

			if !sentEnter {
				ts := time.Now().Format("15:04:05")
				if verbose {
					fmt.Fprintf(os.Stderr, "[%s] Prompt detected (matched: %q)%s\n", ts, matched, dryTag)
				} else {
					fmt.Fprintf(os.Stderr, "[%s] Prompt detected, sending Enter%s\n", ts, dryTag)
				}
				if observeMode {
					// Phase 2 (issue #25): detect-only. Log the match and
					// continue — never touch the pipe. Useful for pattern
					// discovery on new agents.
					fmt.Fprintf(os.Stderr, "[%s] observe: matched=%q agent=%s (send suppressed)\n",
						time.Now().Format("15:04:05"), matched, agentType)
					// Keep sentEnter true so we don't re-log on every tick
					// while the same prompt is on screen.
					sentEnter = true
					continue
				}
				if !dryRun {
					// Phase 0.5 (issue #25): never queue behind a user send.
					// 500ms budget is long enough to win an uncontended lock
					// but short enough to bail out and retry on the next tick
					// (default 2s interval) without flooding the pipe.
					if _, err := daemon.DaemonSendTry(sessionName, "", caller, 500); err != nil {
						if strings.Contains(err.Error(), "lock_timeout") {
							if verbose {
								fmt.Fprintf(os.Stderr, "[%s] approve skipped: busy (user send in flight)\n",
									time.Now().Format("15:04:05"))
							}
							// Do not mark sentEnter / bump entersSent — a
							// skipped tick should not count toward the
							// backoff ceiling.
							continue
						}
						fmt.Fprintf(os.Stderr, "auto-approvals: send failed: %v\n", err)
					}

					// Phase 1 (issue #25): verify the Enter actually closed
					// the modal. Poll the buffer for up to 3s — longer than
					// the 600ms inter-tick interval used inside handleSend so
					// we tolerate Gemini's 200-400ms repaint lag and Codex's
					// inline-box redraw. A resolved prompt means healthy
					// progress, so we reset both sentEnter and entersSent
					// so the backoff ceiling is only approached by genuinely
					// stuck prompts.
					resolved := waitForApprovalResolved(
						defaultVerifier,
						sessionName, caller, agentType,
						3*time.Second, 250*time.Millisecond,
					)
					if resolved {
						if verbose {
							fmt.Fprintf(os.Stderr, "[%s] approve resolved — modal closed\n",
								time.Now().Format("15:04:05"))
						}
						sentEnter = false
						entersSent = 0
						continue
					}
					if verbose {
						fmt.Fprintf(os.Stderr, "[%s] approve unresolved after 3s — counting toward backoff\n",
							time.Now().Format("15:04:05"))
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
