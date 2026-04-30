package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/YuujiKamura/deckpilot/daemon"
	"github.com/YuujiKamura/deckpilot/pipe"
)

// Send sends a message+submit with drain-sequenced delivery:
// INPUT(text) → ACK → RAW_INPUT(\r) → ACK.
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

	// INPUT(text) → ACK → RAW_INPUT(\r) → ACK
	submitID, err := pipe.SendWithSubmit(pipePath, message, "\r")
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		os.Exit(1)
	}

	if submitID == 0 {
		fmt.Fprintf(os.Stderr, "%s: sent (no cmd_id)\n", name)
		return
	}
	fmt.Fprintf(os.Stderr, "%s: ack cmd=%d\n", name, submitID)
}
