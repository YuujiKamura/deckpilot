package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Protect marks a session as advisory-protected (issue #35). A
// protected session is skipped by the cascade kill paths
// (`deckpilot kill <name>` without --force, and `deckpilot shutdown`).
// The daemon persists the list to ~/.deckpilot/protected-sessions.json
// so it survives daemon restarts.
//
// Returns a non-nil error so main can print and exit; success is
// silent on stdout to match the rest of the CLI's "say nothing if
// nothing's wrong" idiom.
func Protect(args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot protect <session>")
		os.Exit(1)
	}
	if err := daemon.EnsureRunning(); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if err := daemon.DaemonProtect(args[0]); err != nil {
		return fmt.Errorf("protect: %w", err)
	}
	fmt.Printf("protected: %s\n", args[0])
	return nil
}
