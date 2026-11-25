package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

type listEntry struct {
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	PipePath   string    `json:"pipe_path"`
	WsURL      string    `json:"ws_url,omitempty"`
	AppRuntime string    `json:"app_runtime"`
	Status     string    `json:"status"`
	Uptime     string    `json:"uptime"`
	Agent      string    `json:"agent,omitempty"`
	Cwd        string    `json:"cwd,omitempty"`
	LaunchedAt string    `json:"launched_at,omitempty"`
	DeadReason string    `json:"dead_reason,omitempty"`
	LastOutput string    `json:"last_output,omitempty"`
	Tabs       []listTab `json:"tabs,omitempty"`
}

// listTab is one tab of a multi-tab window (daemon tabInfo).
type listTab struct {
	Index  int    `json:"index"`
	ID     string `json:"id"`
	Pwd    string `json:"pwd,omitempty"`
	Active bool   `json:"active"`
	State  string `json:"state"`
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

	var sessions []listEntry
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		fmt.Fprintf(os.Stderr, "list: bad json: %v\n", err)
		os.Exit(1)
	}
	enrichLaunchMetadata(sessions)
	if len(args) > 0 && args[0] == "--json" {
		encoded, err := json.Marshal(sessions)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list: encode json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(encoded))
		return
	}

	if len(sessions) == 0 {
		fmt.Println("no active sessions")
		return
	}

	fmt.Printf("%-25s %-6s %-10s %-10s %-10s %s\n", "NAME", "PID", "RUNTIME", "UPTIME", "STATUS", "AGENT")
	for _, s := range sessions {
		fmt.Printf("%-25s %-6d %-10s %-10s %-10s %s\n", s.Name, s.PID, s.AppRuntime, s.Uptime, s.Status, s.Agent)
		if s.DeadReason != "" {
			fmt.Printf("  dead reason: %s\n", s.DeadReason)
			if s.LastOutput != "" {
				fmt.Printf("  last output:\n%s\n", s.LastOutput)
			}
		}
		if s.Cwd != "" {
			fmt.Printf("  cwd: %s", s.Cwd)
			fmt.Println()
		}
		if s.LaunchedAt != "" {
			fmt.Printf("  launched: %s\n", s.LaunchedAt)
		}
		fmt.Print(formatListTabs(s.Name, s.Tabs))
	}
}

// formatListTabs prints one line per tab of a multi-tab window. STATUS above
// is the active tab's; these lines say what the other tabs are doing and how
// to read them.
func formatListTabs(name string, tabs []listTab) string {
	if len(tabs) < 2 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  tabs (* = active; read a tab with: deckpilot show %s --tab <TAB-ID>):\n", name)
	b.WriteString("     INDEX  TAB ID  STATE  CWD\n")
	for _, t := range tabs {
		mark := " "
		if t.Active {
			mark = "*"
		}
		fmt.Fprintf(&b, "   %s %d  %-6s %-8s %s\n", mark, t.Index, t.ID, t.State, t.Pwd)
	}
	return b.String()
}

func enrichLaunchMetadata(sessions []listEntry) {
	for i := range sessions {
		meta, ok, err := ReadLaunchMeta(sessions[i].Name)
		if err != nil || !ok {
			continue
		}
		sessions[i].Agent = meta.Agent
		sessions[i].Cwd = meta.Cwd
		if !meta.LaunchedAt.IsZero() {
			sessions[i].LaunchedAt = meta.LaunchedAt.Format(time.RFC3339)
		}
	}
}
