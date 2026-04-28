package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Unprotect removes the advisory protect flag from a session (issue
// #35). Idempotent: unprotecting a non-protected session is a no-op
// success.
func Unprotect(args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot unprotect <session>")
		os.Exit(1)
	}
	if err := daemon.EnsureRunning(); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if err := daemon.DaemonUnprotect(args[0]); err != nil {
		return fmt.Errorf("unprotect: %w", err)
	}
	fmt.Printf("unprotected: %s\n", args[0])
	return nil
}
