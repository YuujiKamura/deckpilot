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
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	fmt.Fprintf(os.Stderr, "watching %s (Ctrl+C to stop)\n", name)

	for {
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "\nstopped")
			return
		case <-ticker.C:
			content, status, err := daemon.DaemonShow(name, "buffer", caller)
			if err != nil {
				fmt.Fprintf(os.Stderr, "watch: %v\n", err)
				continue
			}

			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
			hasPrompt := strings.Contains(content, "Action Required") ||
				strings.Contains(content, "Enter to select") ||
				strings.Contains(content, "Y/n") ||
				strings.Contains(content, "Allow") ||
				strings.Contains(content, "trust") ||
				strings.Contains(content, "Waiting")

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
