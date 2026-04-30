package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// WaitIdle blocks until the target session reports an idle (or stalled) status.
// Designed for "push-style" completion notifications: run this in the caller's
// background task slot so the caller's harness auto-notifies on exit.
//
// Exit codes:
//
//	0 - target reached idle / stalled
//	1 - usage error / daemon error / session not found
//	2 - timeout elapsed before the target became idle
func WaitIdle(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot wait-idle <session> [--timeout=DURATION] [--poll=DURATION]")
		os.Exit(1)
	}
	name := args[0]

	timeout := 10 * time.Minute
	poll := 500 * time.Millisecond
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--timeout" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "wait-idle: bad --timeout: %v\n", err)
				os.Exit(1)
			}
			timeout = d
			i++
		case len(a) > len("--timeout=") && a[:len("--timeout=")] == "--timeout=":
			d, err := time.ParseDuration(a[len("--timeout="):])
			if err != nil {
				fmt.Fprintf(os.Stderr, "wait-idle: bad --timeout: %v\n", err)
				os.Exit(1)
			}
			timeout = d
		case a == "--poll" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "wait-idle: bad --poll: %v\n", err)
				os.Exit(1)
			}
			poll = d
			i++
		case len(a) > len("--poll=") && a[:len("--poll=")] == "--poll=":
			d, err := time.ParseDuration(a[len("--poll="):])
			if err != nil {
				fmt.Fprintf(os.Stderr, "wait-idle: bad --poll: %v\n", err)
				os.Exit(1)
			}
			poll = d
		default:
			fmt.Fprintf(os.Stderr, "wait-idle: unknown arg: %s\n", a)
			os.Exit(1)
		}
	}

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(timeout)
	sawSession := false
	for {
		status, found, err := lookupStatus(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wait-idle: %v\n", err)
			os.Exit(1)
		}
		if found {
			sawSession = true
			if status == "idle" || status == "stalled" {
				fmt.Printf("%s: %s\n", name, status)
				return
			}
		} else if sawSession {
			// Session disappeared after we saw it — treat as terminal.
			fmt.Printf("%s: gone\n", name)
			return
		}

		if time.Now().After(deadline) {
			if !sawSession {
				fmt.Fprintf(os.Stderr, "wait-idle: session %q not found within %s\n", name, timeout)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "wait-idle: timeout after %s (last status=%q)\n", timeout, status)
			os.Exit(2)
		}
		time.Sleep(poll)
	}
}

func lookupStatus(name string) (string, bool, error) {
	raw, err := daemon.DaemonList()
	if err != nil {
		return "", false, err
	}
	var sessions []listEntry
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		return "", false, fmt.Errorf("bad json: %w", err)
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.Status, true, nil
		}
	}
	return "", false, nil
}
