package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

type listEntry struct {
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	PipePath   string `json:"pipe_path"`
	AppRuntime string `json:"app_runtime"`
	Status     string `json:"status"`
	Uptime     string `json:"uptime"`
}

// List prints all active sessions as a table.
func List(args []string) {
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	raw, err := daemon.DaemonList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		os.Exit(1)
	}

	if len(args) > 0 && args[0] == "--json" {
		fmt.Println(raw)
		return
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

	fmt.Printf("%-25s %-6s %-10s %-10s %s\n", "NAME", "PID", "RUNTIME", "UPTIME", "STATUS")
	for _, s := range sessions {
		fmt.Printf("%-25s %-6d %-10s %-10s %s\n", s.Name, s.PID, s.AppRuntime, s.Uptime, s.Status)
	}
}
