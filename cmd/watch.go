package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// watchEntry holds per-session display data for the dashboard.
type watchEntry struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Pending    string `json:"pending"`
	LastChange string `json:"last_change"`
	Tail       string `json:"tail"`
}

// Watch monitors one or all sessions and displays a live dashboard.
// No auto-approval is performed; use auto-approvals for that.
//
// Usage:
//
//	deckpilot watch [session] [--once] [--json]
func Watch(args []string) {
	filterSession := ""
	flagOnce := false
	flagJSON := false

	for _, a := range args {
		switch a {
		case "--once":
			flagOnce = true
		case "--json":
			flagJSON = true
		default:
			if !strings.HasPrefix(a, "-") && filterSession == "" {
				filterSession = a
			}
		}
	}

	// Deprecation warning: watch <session> の個別セッション指定は監視専用に変更されました。
	// 自動承認が必要な場合は auto-approvals を使ってください。
	if filterSession != "" && os.Getenv("DECKPILOT_SUPPRESS_DEPRECATION") == "" {
		fmt.Fprintf(os.Stderr, "[deprecation] `deckpilot watch <session>` は監視専用に変更されました。\n")
		fmt.Fprintf(os.Stderr, "              自動承認が必要な場合は `deckpilot auto-approvals %s` を使ってください。\n", filterSession)
		fmt.Fprintf(os.Stderr, "              この警告は次期マイナーバージョンで削除されます。\n")
		fmt.Fprintf(os.Stderr, "              警告を抑制するには DECKPILOT_SUPPRESS_DEPRECATION=1 を設定してください。\n")
	}

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	caller := getCaller()

	// doSnapshot renders one dashboard frame. It returns nil on success,
	// an error wrapping daemon.ErrSessionNotFound when a filtered session is
	// absent (permanent), or another error when the daemon could not be read
	// (transient). The caller decides what to print and whether to keep going.
	doSnapshot := func() error {
		raw, err := watchDaemonList()
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}

		type listEntry struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		var sessions []listEntry
		if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
			return fmt.Errorf("bad json: %w", err)
		}

		var entries []watchEntry
		for _, s := range sessions {
			if filterSession != "" && s.Name != filterSession {
				continue
			}

			content, _, showErr := watchDaemonShow(s.Name, "buffer", caller)
			pending := "-"
			tail := ""

			if showErr == nil && strings.TrimSpace(content) != "" {
				if matched, found := DetectApprovalPrompt(content); found {
					pending = "YES(" + matched + ")"
				}

				lines := strings.Split(content, "\n")
				// Trim trailing empty lines
				for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
					lines = lines[:len(lines)-1]
				}
				start := len(lines) - 1
				if start < 0 {
					start = 0
				}
				tail = strings.TrimSpace(lines[start])
				// Truncate long tails for display
				if len(tail) > 60 {
					tail = tail[:57] + "..."
				}
			}

			entries = append(entries, watchEntry{
				Name:       s.Name,
				Status:     s.Status,
				Pending:    pending,
				LastChange: time.Now().Format("15:04:05"),
				Tail:       tail,
			})
		}

		if len(entries) == 0 {
			if filterSession != "" {
				return daemon.SessionNotFound(filterSession)
			}
			fmt.Fprintln(os.Stderr, "no active sessions")
			return nil
		}

		if flagJSON {
			for _, e := range entries {
				b, _ := json.Marshal(e)
				fmt.Println(string(b))
			}
		} else {
			// Clear screen for dashboard refresh (only in loop mode)
			if !flagOnce {
				fmt.Print("\033[H\033[2J")
			}
			fmt.Printf("%-25s %-8s %-20s %-10s %s\n", "NAME", "STATUS", "PENDING", "LAST-CHANGE", "TAIL")
			fmt.Println(strings.Repeat("-", 90))
			for _, e := range entries {
				fmt.Printf("%-25s %-8s %-20s %-10s %s\n",
					e.Name, e.Status, e.Pending, e.LastChange, e.Tail)
			}
		}
		return nil
	}

	if flagOnce {
		if err := doSnapshot(); err != nil {
			fmt.Fprintf(os.Stderr, "watch: %v\n", err)
			os.Exit(1)
		}
		return
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	if !flagJSON {
		fmt.Fprintf(os.Stderr, "watching sessions (2s poll, Ctrl+C to stop)\n")
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// A watch bound to one session stops when that session is gone; watching
	// all sessions keeps going while the list is empty. Either way a daemon
	// that stays unreachable ends the loop instead of spamming forever.
	guard := newSessionPollGuard(transientLimitFor(2*time.Second, defaultTransientBudget))
	step := func() bool {
		verdict, line := guard.Observe(doSnapshot())
		if line != "" {
			fmt.Fprintf(os.Stderr, "watch: %s\n", line)
		}
		return verdict != pollStop
	}

	// Run once immediately before first tick
	if !step() {
		os.Exit(1)
	}

	for {
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "\nstopped")
			return
		case <-ticker.C:
			if !step() {
				os.Exit(1)
			}
		}
	}
}

// Seams for tests: production reads sessions through the daemon.
var (
	watchDaemonList = daemon.DaemonList
	watchDaemonShow = daemon.DaemonShow
)
