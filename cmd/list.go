package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

type listEntry struct {
	Name       string `json:"name"`
	PipePath   string `json:"pipe_path"`
	AppRuntime string `json:"app_runtime"`
	Status     string `json:"status"`
}

// List prints all active sessions as a table.
func List() {
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	raw, err := daemon.DaemonList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		os.Exit(1)
	}

	var sessions []listEntry
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		fmt.Fprintf(os.Stderr, "list: bad json: %v\n", err)
		os.Exit(1)
	}

	if len(sessions) == 0 {
		fmt.Println("no active sessions")
		return
	}

	fmt.Printf("%-25s %-10s %s\n", "NAME", "RUNTIME", "STATUS")
	for _, s := range sessions {
		fmt.Printf("%-25s %-10s %s\n", s.Name, s.AppRuntime, s.Status)
	}
}
