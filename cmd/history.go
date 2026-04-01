package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
	"github.com/YuujiKamura/deckpilot/pipe"
)

// History prints the command history for a session.
// args[0] = session name.
func History(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot history <session>")
		os.Exit(1)
	}
	name := args[0]

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	encoded, err := daemon.DaemonHistory(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "history: %v\n", err)
		os.Exit(1)
	}

	content, err := pipe.Base64Decode(encoded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(content)
}
