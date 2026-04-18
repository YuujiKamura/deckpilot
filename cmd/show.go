// Package cmd: show — 個別セッションの詳細バッファを取得する。
// 一覧や承認処理は行わない。承認処理が必要な場合は auto-approvals を使うこと。
package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// formatShowHeader returns the bracketed header line emitted as the first line of show output.
func formatShowHeader(name string, now time.Time, uptime, lastAct string) string {
	ts := now.Format("2006-01-02 Mon 15:04 MST")
	if uptime == "" && lastAct == "" {
		return fmt.Sprintf("[%s | now %s]", name, ts)
	}
	if lastAct == "" {
		return fmt.Sprintf("[%s | now %s | uptime %s]", name, ts, uptime)
	}
	return fmt.Sprintf("[%s | now %s | uptime %s | last-act %s]", name, ts, uptime, lastAct)
}

// Show retrieves an individual session's buffer or history.
// It intentionally does NOT send any input or perform auto-approval.
// For auto-approval use: deckpilot auto-approvals <session>
func Show(args []string) {
	caller := getCaller()

	name := ""
	mode := "buffer"
	tail := 50
	follow := false

	// Parse flags and positional args
	remaining := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--history":
			mode = "history"
		case a == "--follow" || a == "-f":
			follow = true
		case a == "--tail":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "show: --tail requires a number")
				os.Exit(1)
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				fmt.Fprintf(os.Stderr, "show: --tail value must be a positive integer, got %q\n", args[i])
				os.Exit(1)
			}
			tail = n
		case strings.HasPrefix(a, "--tail="):
			val := strings.TrimPrefix(a, "--tail=")
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				fmt.Fprintf(os.Stderr, "show: --tail value must be a positive integer, got %q\n", val)
				os.Exit(1)
			}
			tail = n
		case a == "--help" || a == "-h":
			printShowHelp()
			return
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "show: unknown flag %q\n", a)
			os.Exit(1)
		default:
			remaining = append(remaining, a)
		}
	}

	// Positional args: [session] [history]  (backward compat)
	if len(remaining) >= 1 {
		name = remaining[0]
	}
	if len(remaining) >= 2 && remaining[1] == "history" {
		mode = "history"
	}

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	_ = tail // tail is reserved for future daemon-side filtering; currently full buffer is fetched

	if follow {
		runShowFollow(name, mode, caller)
		return
	}

	content, status, uptime, lastAct, err := daemon.DaemonShow(name, mode, caller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "show: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(formatShowHeader(name, time.Now().In(cmdJST), uptime, lastAct))
	fmt.Fprintf(os.Stderr, "[%s]\n", status)
	fmt.Print(content)
}

// runShowFollow polls the session buffer every 2 seconds and prints new content.
// It performs display only — no Enter or approval is ever sent.
// For auto-approval use: deckpilot auto-approvals <session>
func runShowFollow(name, mode, caller string) {
	var last string
	for {
		content, status, _, _, err := daemon.DaemonShow(name, mode, caller)
		if err != nil {
			fmt.Fprintf(os.Stderr, "show: %v\n", err)
			os.Exit(1)
		}
		if content != last {
			fmt.Fprintf(os.Stderr, "[%s]\n", status)
			fmt.Print(content)
			last = content
		}
		time.Sleep(2 * time.Second)
	}
}

func printShowHelp() {
	fmt.Print(`Usage: deckpilot show [session] [--tail N] [--history] [--follow]

  Retrieve an individual session's buffer (view-only).
  This command never sends input or auto-approves prompts.
  For auto-approval use: deckpilot auto-approvals <session>

Flags:
  --tail N      Show last N lines (default: 50). Currently fetches full buffer.
  --history     Show history instead of live buffer (equivalent to positional arg "history")
  --follow, -f  Poll every 2s and print updated buffer. Display-only, no approval.
  --help, -h    Show this help message

Examples:
  deckpilot show
  deckpilot show my-session
  deckpilot show my-session --tail 100
  deckpilot show my-session --history
  deckpilot show my-session --follow
`)
}
