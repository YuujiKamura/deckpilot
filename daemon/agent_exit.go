package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var windowsPromptLine = regexp.MustCompile(`^[A-Za-z]:\\[^\r\n]*>\s*$`)

// A shell prompt only means "the agent exited" after the agent command was
// actually sent. launch writes the metadata before the shell is ready, so the
// first screen of every launch is a bare prompt; treating that as an exit
// killed the watcher one second into the launch (2026-11-14, ghostty-6520).
func isLaunchedAgentAtShellPrompt(session, buffer string) bool {
	if !hasLaunchMetadata(session) {
		return false
	}
	lines := strings.Split(buffer, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return windowsPromptLine.MatchString(line)
	}
	return false
}

func hasLaunchMetadata(session string) bool {
	home, err := daemonUserHome()
	if err != nil {
		home = os.TempDir()
	}
	path := filepath.Join(home, ".deckpilot", "launch-meta", strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(session)+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var meta struct {
		SessionName  string `json:"session_name"`
		Agent        string `json:"agent"`
		AgentStarted bool   `json:"agent_started"`
	}
	if json.Unmarshal(body, &meta) != nil {
		return false
	}
	return meta.SessionName == session && meta.Agent != "" && meta.AgentStarted
}
