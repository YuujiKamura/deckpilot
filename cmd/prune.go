package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

func Prune(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot prune <dead-session>")
		os.Exit(1)
	}
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}
	if err := daemon.DaemonPrune(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "prune: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("pruned dead session tracking: %s\n", args[0])
}
