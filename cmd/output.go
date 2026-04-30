package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
	"github.com/YuujiKamura/deckpilot/pipe"
)

// Output prints the last N lines of output from a session.
// args[0] = session name.
func Output(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot output <session>")
		os.Exit(1)
	}
	name := args[0]

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	encoded, err := daemon.DaemonOutput(name, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "output: %v\n", err)
		os.Exit(1)
	}

	content, err := pipe.Base64Decode(encoded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(content)
}
