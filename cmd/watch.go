package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

func Watch(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot watch <session>")
		os.Exit(1)
	}
	name := args[0]
	caller := getCaller()

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	lastHash := ""
	sentEnter := false // true after we send Enter; reset when prompt disappears
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	fmt.Fprintf(os.Stderr, "watching %s (2s poll, Ctrl+C to stop)\n", name)

	for {
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "\nstopped")
			return
		case <-ticker.C:
			content, status, err := daemon.DaemonShow(name, "buffer", caller)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] watch: %v (retrying...)\n", time.Now().Format("15:04:05"), err)
				// Session may have temporarily disconnected, wait and retry
				time.Sleep(2 * time.Second)
				continue
			}

			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

			// Skip empty buffer — pipe may not be ready yet
			if strings.TrimSpace(content) == "" {
				continue
			}

			// Only check last 15 lines for prompts to avoid false positives from log output
			promptLines := content
			if lines := strings.Split(content, "\n"); len(lines) > 15 {
				promptLines = strings.Join(lines[len(lines)-15:], "\n")
			}
			hasPrompt := strings.Contains(promptLines, "Action Required") ||
				strings.Contains(promptLines, "Enter to select") ||
				strings.Contains(promptLines, "Y/n") ||
				strings.Contains(promptLines, "Allow") ||
				strings.Contains(promptLines, "trust") ||
				strings.Contains(promptLines, "Waiting")

			// Reset flag once prompt disappears (agent moved on)
			if !hasPrompt {
				sentEnter = false
			}

			// Auto-approve: send Enter once per prompt appearance
			if hasPrompt && !sentEnter {
				fmt.Fprintf(os.Stderr, "[%s] Prompt detected, sending Enter\n", time.Now().Format("15:04:05"))
				if _, err := daemon.DaemonSend(name, "", caller); err != nil {
					fmt.Fprintf(os.Stderr, "watch: send failed: %v\n", err)
				}
				sentEnter = true
			}

			// Show tail on change
			if hash != lastHash {
				lastHash = hash
				lines := strings.Split(content, "\n")
				start := len(lines) - 5
				if start < 0 {
					start = 0
				}
				fmt.Fprintf(os.Stderr, "[%s] [%s]\n", time.Now().Format("15:04:05"), status)
				for _, l := range lines[start:] {
					fmt.Fprintln(os.Stderr, l)
				}
			}
		}
	}
}
