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
	"github.com/YuujiKamura/deckpilot/pipe"
)

// Show retrieves an individual session's buffer or history.
// It intentionally does NOT send any input or perform auto-approval.
// For auto-approval use: deckpilot auto-approvals <session>
func Show(args []string) {
	caller := getCaller()

	name := ""
	mode := "buffer"
	tail := 50
	follow := false
	tab := ""
	listTabs := false

	// Parse flags and positional args
	remaining := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--history":
			mode = "history"
		case a == "--follow" || a == "-f":
			follow = true
		case a == "--tabs":
			listTabs = true
		case a == "--tab":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "show: --tab requires a tab index (0, 1, ...) or id=<tab id>")
				os.Exit(1)
			}
			tab = args[i]
		case strings.HasPrefix(a, "--tab="):
			tab = strings.TrimPrefix(a, "--tab=")
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

	if tab != "" && !pipe.ValidTab(tab) {
		fmt.Fprintf(os.Stderr, "show: invalid --tab %q (want an index like 0 or id=t_001)\n", tab)
		os.Exit(1)
	}
	if listTabs && (tab != "" || follow || mode == "history") {
		fmt.Fprintln(os.Stderr, "show: --tabs cannot be combined with --tab, --history or --follow")
		os.Exit(1)
	}

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	if listTabs {
		content, _, err := daemon.DaemonShowTab(name, "tabs", caller, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "show: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(formatTabList(content))
		return
	}

	if follow {
		runShowFollow(name, mode, caller, tab)
		return
	}

	content, status, err := daemon.DaemonShowTab(name, mode, caller, tab)
	if err != nil {
		fmt.Fprintf(os.Stderr, "show: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[%s]\n", status)
	// A terminal buffer ends in blank rows; without trimming, the last N
	// lines of a mostly empty screen are all blank and read as "nothing".
	if mode != "history" {
		content = tailLines(trimScreen(content), tail) + "\n"
	}
	marked, summary := markInputBox(content)
	printInputBoxSummary(summary)
	fmt.Print(marked)
}

// printInputBoxSummary echoes what markInputBox tagged onto stderr — a trusted
// side channel separate from the (potentially terminal-controlled) stdout
// buffer. A reader can cross-check the in-band "[入力欄/未送信]" marker against
// this line: deckpilot tagged exactly one input box, and this is its content.
func printInputBoxSummary(summary string) {
	if summary == "" {
		fmt.Fprintln(os.Stderr, "[input-box] none in this view")
		return
	}
	fmt.Fprintf(os.Stderr, "[input-box] tagged: > %s\n", summary)
}

// runShowFollow polls the session buffer every 2 seconds and prints new content.
// It performs display only — no Enter or approval is ever sent.
// For auto-approval use: deckpilot auto-approvals <session>
func runShowFollow(name, mode, caller, tab string) {
	var last string
	for {
		content, status, err := daemon.DaemonShowTab(name, mode, caller, tab)
		if err != nil {
			fmt.Fprintf(os.Stderr, "show: %v\n", err)
			os.Exit(1)
		}
		// Compare on the raw (pre-mark) content so the marker rewrite does
		// not register as a change on every poll.
		if content != last {
			fmt.Fprintf(os.Stderr, "[%s]\n", status)
			marked, summary := markInputBox(content)
			printInputBoxSummary(summary)
			fmt.Print(marked)
			last = content
		}
		time.Sleep(2 * time.Second)
	}
}

func printShowHelp() {
	fmt.Print(`Usage: deckpilot show [session] [--tail N] [--history] [--follow] [--tab T | --tabs]

  Retrieve an individual session's buffer (view-only).
  This command never sends input or auto-approves prompts.
  For auto-approval use: deckpilot auto-approvals <session>

Flags:
  --tail N      Show last N non-blank-trimmed lines (default: 50).
  --history     Show history instead of live buffer (equivalent to positional arg "history")
  --follow, -f  Poll every 2s and print updated buffer. Display-only, no approval.
  --tab T       Read tab T instead of the active tab. T is an index (0, 1, ...)
                or a tab id (id=t_001). Works with --tail, --history, --follow.
  --tabs        List the window's tabs (index, id, cwd; * = active tab).
  --help, -h    Show this help message

Examples:
  deckpilot show
  deckpilot show my-session
  deckpilot show my-session --tail 100
  deckpilot show my-session --history
  deckpilot show my-session --follow
  deckpilot show my-session --tabs
  deckpilot show my-session --tab 0 --tail 100
  deckpilot show my-session --tab id=t_001 --history
`)
}

// formatTabList turns a raw LIST_TABS reply into one line per tab:
// "* 1  t_001  <cwd>" with "*" marking the active tab. Lines it does not
// understand are passed through so nothing from the server is hidden.
func formatTabList(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	active := ""
	var b strings.Builder
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		f := strings.Split(ln, "|")
		switch {
		case f[0] == "LIST_TABS" && len(f) >= 3:
			active = f[2]
			fmt.Fprintf(&b, "tabs: %s (active: %s)\n", f[1], f[2])
		case f[0] == "TAB" && len(f) >= 3:
			mark := " "
			if f[1] == active {
				mark = "*"
			}
			pwd := ""
			for _, kv := range f[3:] {
				if strings.HasPrefix(kv, "pwd=") {
					pwd = strings.TrimPrefix(kv, "pwd=")
				}
			}
			fmt.Fprintf(&b, "%s %s  %s  %s\n", mark, f[1], f[2], pwd)
		default:
			b.WriteString(ln + "\n")
		}
	}
	return b.String()
}
