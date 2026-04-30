package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Send sends a message to a named session.
// args[0] = session name, args[1:] = message words joined with space.
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

	if err := daemon.DaemonSend(name, message); err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "sent to %s\n", name)
}
