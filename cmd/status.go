package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
	"github.com/YuujiKamura/deckpilot/pipe"
)

// Status prints the status and last few lines of output for a session.
func Status(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot status <session>")
		os.Exit(1)
	}
	name := args[0]

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	raw, err := daemon.DaemonList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		os.Exit(1)
	}

	var sessions []listEntry
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		fmt.Fprintf(os.Stderr, "status: bad json: %v\n", err)
		os.Exit(1)
	}

	for _, s := range sessions {
		if s.Name == name {
			fmt.Printf("session: %s\n", s.Name)
			fmt.Printf("status:  %s\n", s.Status)

			encoded, err := daemon.DaemonOutput(name, 10)
			if err != nil {
				return
			}
			content, err := pipe.Base64Decode(encoded)
			if err != nil {
				return
			}
			if content != "" {
				fmt.Println("--- last output ---")
				fmt.Print(content)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "session not found: %s\n", name)
	os.Exit(1)
}
