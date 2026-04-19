package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Tag updates metadata for a session.
// Usage: deckpilot tag <session> --model <name>
func Tag(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot tag <session> [--model name]")
		os.Exit(1)
	}

	name := ""
	tags := make(map[string]string)

	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			switch a {
			case "--model":
				i++
				if i >= len(args) {
					fmt.Fprintln(os.Stderr, "tag: --model requires a name")
					os.Exit(1)
				}
				tags["model"] = args[i]
			default:
				fmt.Fprintf(os.Stderr, "tag: unknown flag %q\n", a)
				os.Exit(1)
			}
		} else if name == "" {
			name = a
		}
	}

	if name == "" {
		fmt.Fprintln(os.Stderr, "tag: session name required")
		os.Exit(1)
	}

	if len(tags) == 0 {
		fmt.Fprintln(os.Stderr, "tag: no metadata specified (use --model)")
		os.Exit(1)
	}

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	if err := daemon.DaemonTag(name, tags); err != nil {
		fmt.Fprintf(os.Stderr, "tag: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("tagged session %q: %v\n", name, tags)
}
