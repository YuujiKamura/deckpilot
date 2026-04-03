package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

func Show(args []string) {
	caller := getCaller()

	name := ""
	mode := "buffer"

	if len(args) >= 1 {
		name = args[0]
	}
	if len(args) >= 2 && args[1] == "history" {
		mode = "history"
	}

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	content, status, err := daemon.DaemonShow(name, mode, caller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "show: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[%s]\n", status)
	fmt.Print(content)
}
