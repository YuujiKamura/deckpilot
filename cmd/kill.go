package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Kill terminates the OS process backing a session (issue #35).
//
// Flag handling is hand-rolled to match the rest of cmd/ (see
// launch.go). The first non-flag positional is the session name; the
// only flag is --force, which bypasses the protect check. We reject
// extra positionals rather than silently accepting them so a typo
// like `kill --foce <name>` doesn't end up parsing "--foce" as the
// session name.
func Kill(args []string) error {
	force := false
	var session string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force", "-f":
			force = true
		default:
			if session != "" {
				fmt.Fprintf(os.Stderr, "kill: unexpected argument %q\n", args[i])
				os.Exit(1)
			}
			session = args[i]
		}
	}
	if session == "" {
		fmt.Fprintln(os.Stderr, "usage: deckpilot kill <session> [--force]")
		os.Exit(1)
	}
	if err := daemon.EnsureRunning(); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if err := daemon.DaemonKill(session, force); err != nil {
		// Surface the daemon error verbatim — E2E gate #3 greps
		// stderr for the literal string "protected".
		return fmt.Errorf("kill: %w", err)
	}
	fmt.Printf("killed: %s\n", session)
	return nil
}
