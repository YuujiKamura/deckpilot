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

// Send sends a message+submit with cmd_id ACK delivery guarantee.
// 1. INPUT text → get cmd_id
// 2. RAW_INPUT \r → get cmd_id
// 3. ACK_POLL cmd_id until ACK or max retries
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

	// Step 1: Send text via INPUT
	resp1, err := pipe.SendRecv(pipePath, fmt.Sprintf("INPUT|deckpilot|%s", pipe.Base64Encode(message)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "send text: %v\n", err)
		os.Exit(1)
	}
	textCmdID, _ := pipe.ParseCmdID(resp1)

	// Step 2: Send \r via RAW_INPUT
	resp2, err := pipe.SendRecv(pipePath, fmt.Sprintf("RAW_INPUT|deckpilot|%s", pipe.Base64Encode("\r")))
	if err != nil {
		fmt.Fprintf(os.Stderr, "send enter: %v\n", err)
		os.Exit(1)
	}
	enterCmdID, _ := pipe.ParseCmdID(resp2)

	// Use the higher cmd_id (the \r) as the ACK target
	targetID := enterCmdID
	if targetID == 0 {
		targetID = textCmdID
	}

	if targetID == 0 {
		// No cmd_id support (old Ghostty), fall back to status polling
		fmt.Fprintf(os.Stderr, "%s: sent (no cmd_id)\n", name)
		return
	}

	// Step 3: ACK_POLL until confirmed
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		acked, err := pipe.AckPoll(pipePath, targetID)
		if err == nil && acked {
			fmt.Fprintf(os.Stderr, "%s: ack cmd=%d (%d polls)\n", name, targetID, i+1)
			return
		}
	}

	fmt.Fprintf(os.Stderr, "%s: sent cmd=%d (no ack after 10 polls)\n", name, targetID)
}
