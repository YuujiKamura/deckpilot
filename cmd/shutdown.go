package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Shutdown gracefully shuts down the daemon
func Shutdown() {
	resp, err := daemon.DaemonShutdown()
	if err != nil {
		fmt.Fprintf(os.Stderr, "shutdown error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("daemon: %s\n", resp)
}
