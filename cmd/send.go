package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Send sends a message+submit via the daemon (which handles pipe resolution,
// watcher pause, drain-sequenced delivery, and caller tracking).
func Send(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot send <session> <message...>")
		os.Exit(1)
	}
	name := args[0]
	message := strings.Join(args[1:], " ")
	caller := getCaller()

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	result, err := daemon.DaemonSend(name, message, caller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "%s: %s\n", name, result)
}
