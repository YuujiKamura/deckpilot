package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/YuujiKamura/deckpilot/daemon"
	"github.com/YuujiKamura/deckpilot/pipe"
)

const sendUsage = "usage: deckpilot send <session> [--tab T] <message...>"

// Send sends a message+submit via the daemon (which handles pipe resolution,
// watcher pause, drain-sequenced delivery, and caller tracking).
//
// --tab T (an index or id=t_001, right after the session name) types into
// that tab instead of the active one.
func Send(args []string) {
	name, tab, msgArgs, err := parseSendArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "send: "+err.Error())
		fmt.Fprintln(os.Stderr, sendUsage)
		os.Exit(1)
	}
	// Restore MSYS2-mangled slash commands
	for i := range msgArgs {
		msgArgs[i] = unmangleMSYS(msgArgs[i])
	}
	message := strings.Join(msgArgs, " ")
	caller := getCaller()

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	var result string
	if tab != "" {
		result, err = daemon.DaemonSendTab(name, message, caller, tab)
	} else {
		result, err = daemon.DaemonSend(name, message, caller)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "%s: %s\n", name, result)
}

// parseSendArgs splits "<session> [--tab T | --tab=T] <message...>". The
// flag is only read right after the session name, so a message that
// contains "--tab" is sent as written.
func parseSendArgs(args []string) (name, tab string, msg []string, err error) {
	if len(args) < 2 {
		return "", "", nil, fmt.Errorf("session and message required")
	}
	name, rest := args[0], args[1:]
	switch {
	case rest[0] == "--tab":
		if len(rest) < 2 {
			return "", "", nil, fmt.Errorf("--tab requires a tab index (0, 1, ...) or id=<tab id>")
		}
		tab, rest = rest[1], rest[2:]
	case strings.HasPrefix(rest[0], "--tab="):
		tab, rest = strings.TrimPrefix(rest[0], "--tab="), rest[1:]
	}
	if tab != "" || strings.HasPrefix(args[1], "--tab") {
		if !pipe.ValidTab(tab) {
			return "", "", nil, fmt.Errorf("invalid --tab %q (want an index like 0 or id=t_001)", tab)
		}
		if len(rest) == 0 {
			return "", "", nil, fmt.Errorf("message required after --tab %s", tab)
		}
	}
	return name, tab, rest, nil
}
