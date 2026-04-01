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

// Send sends a message to a named session.
// Sends text via INPUT then \r via RAW_INPUT directly from this process
// (not through daemon) because daemon-process RAW_INPUT doesn't trigger submit.
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

	// Resolve pipe path from daemon
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

	// Send text via INPUT, wait for Ghostty to process, then \r via RAW_INPUT
	if err := pipe.SendKeys(pipePath, message); err != nil {
		fmt.Fprintf(os.Stderr, "send text: %v\n", err)
		os.Exit(1)
	}
	time.Sleep(500 * time.Millisecond)
	if err := pipe.SendRaw(pipePath, []byte("\r")); err != nil {
		fmt.Fprintf(os.Stderr, "send enter: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%s: submitted\n", name)
}
