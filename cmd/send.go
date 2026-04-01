package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
	"github.com/YuujiKamura/deckpilot/pipe"
)

// Send sends a message+submit with delivery guarantee.
// 1. INPUT text
// 2. RAW_INPUT \r
// 3. Poll watcher status — if active, done (ACK)
// 4. If still idle, retry \r
func Send(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot send <session> <message...>")
		os.Exit(1)
	}
	name := args[0]
	message := strings.Join(args[1:], " ")

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	// Resolve pipe path
	raw, err := daemon.DaemonList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		os.Exit(1)
	}
	var sessions []struct {
		Name     string `json:"name"`
		PipePath string `json:"pipe_path"`
	}
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}
	var pipePath string
	for _, s := range sessions {
		if s.Name == name {
			pipePath = s.PipePath
			break
		}
	}
	if pipePath == "" {
		fmt.Fprintf(os.Stderr, "session not found: %s\n", name)
		os.Exit(1)
	}

	// Step 1: Send text
	if err := pipe.SendKeys(pipePath, message); err != nil {
		fmt.Fprintf(os.Stderr, "send text: %v\n", err)
		os.Exit(1)
	}

	// Step 2: ACK loop — send \r, check status, retry
	for i := 0; i < 10; i++ {
		pipe.SendRaw(pipePath, []byte("\r"))
		time.Sleep(300 * time.Millisecond)

		status, err := daemon.DaemonStatus(name)
		if err == nil && status == "active" {
			fmt.Fprintf(os.Stderr, "%s: submitted (%d)\n", name, i+1)
			return
		}
	}

	fmt.Fprintf(os.Stderr, "%s: sent (no ack)\n", name)
}
