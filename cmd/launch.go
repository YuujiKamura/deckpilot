package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Launch starts a Ghostty window running the specified agent, waits for the
// session to appear, handles trust confirmation, waits for ready, and sends
// the prompt. Prints the session name on success.
func Launch(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: deckpilot launch <agent> <prompt...> [--cwd DIR]")
		os.Exit(1)
	}

	agentName := args[0]
	agent, ok := agents[agentName]
	if !ok {
		names := make([]string, 0, len(agents))
		for k := range agents {
			names = append(names, k)
		}
		fmt.Fprintf(os.Stderr, "unknown agent: %s (available: %s)\n", agentName, strings.Join(names, ", "))
		os.Exit(1)
	}

	// Parse --cwd flag and collect prompt
	cwd, _ := os.Getwd()
	var promptParts []string
	for i := 1; i < len(args); i++ {
		if args[i] == "--cwd" && i+1 < len(args) {
			cwd = args[i+1]
			i++
		} else {
			promptParts = append(promptParts, args[i])
		}
	}
	prompt := strings.Join(promptParts, " ")

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	// Snapshot current sessions
	knownSessions := listSessionNames()

	// Find and launch Ghostty
	ghosttyExe := findGhostty()
	if ghosttyExe == "" {
		fmt.Fprintln(os.Stderr, "ghostty not found. Set GHOSTTY_EXE or install ghostty on PATH")
		os.Exit(1)
	}

	// Launch plain Ghostty, then send commands via CP after session is up.
	// Using -e causes IO thread errors on debug builds, so we launch bare
	// and type the command into the shell.
	cmd := exec.Command(ghosttyExe)
	cmd.Dir = cwd
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS = 0x00000008
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "launch ghostty: %v\n", err)
		os.Exit(1)
	}
	// Once started, completely detach. Do not Wait(), do not keep handles.
	// The daemon will pick up the session via file scanning.
	if cmd.Process != nil {
		cmd.Process.Release()
	}
	fmt.Fprintf(os.Stderr, "launched: %s\n", ghosttyExe)

	// Wait for new session to appear
	sessionName, err := waitForNewSession(knownSessions, 15*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session detect: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "session: %s\n", sessionName)

	// Wait for shell prompt (give the terminal a moment to render)
	time.Sleep(2 * time.Second)

	// Send cd + agent launch command.
	// Ghostty on Windows defaults to cmd.exe, so use cmd syntax.
	launchCmd := fmt.Sprintf("cd /d \"%s\" && %s %s",
		cwd,
		agent.Cmd,
		strings.Join(agent.Args, " "))
	caller := strconv.Itoa(os.Getppid())
	_, err = daemon.DaemonSend(sessionName, launchCmd, caller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send launch cmd: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "sent launch: %s %s\n", agent.Cmd, strings.Join(agent.Args, " "))

	// Wait for trust or ready
	err = waitForReady(sessionName, agent, 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ready wait: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "ready: %s\n", agentName)

	// Send prompt
	result, err := daemon.DaemonSend(sessionName, prompt, caller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "sent: %s\n", result)

	// Print session name to stdout for scripting
	fmt.Println(sessionName)
}

func findGhostty() string {
	// 1. GHOSTTY_EXE env
	if exe := os.Getenv("GHOSTTY_EXE"); exe != "" {
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}

	// 2. PATH
	if p, err := exec.LookPath("ghostty"); err == nil {
		return p
	}

	// 3. Known build locations
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "ghostty-win", "zig-out-winui3", "bin", "ghostty.exe"),
		filepath.Join(home, "ghostty-win", "zig-out-winui3-release", "bin", "ghostty.exe"),
		filepath.Join(home, "ghostty-win", "zig-out-winui3-build", "bin", "ghostty.exe"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	return ""
}

func listSessionNames() []string {
	raw, err := daemon.DaemonList()
	if err != nil {
		return nil
	}
	var sessions []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		return nil
	}
	names := make([]string, len(sessions))
	for i, s := range sessions {
		names[i] = s.Name
	}
	return names
}

func waitForNewSession(known []string, timeout time.Duration) (string, error) {
	knownSet := make(map[string]bool, len(known))
	for _, n := range known {
		knownSet[n] = true
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		current := listSessionNames()
		for _, name := range current {
			if !knownSet[name] {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("no new session detected within %v", timeout)
}

func waitForReady(session string, agent AgentDef, timeout time.Duration) error {
	caller := strconv.Itoa(os.Getppid())
	trustHandled := agent.TrustStr == ""
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)

		output, _, err := daemon.DaemonShow(session, "buffer", caller)
		if err != nil {
			continue
		}

		// Handle trust confirmation
		if !trustHandled && strings.Contains(output, agent.TrustStr) {
			// Send Enter to accept trust
			daemon.DaemonSend(session, "", caller)
			trustHandled = true
			time.Sleep(2 * time.Second)
			continue
		}

		// Check for ready – once the ready string is visible, the agent
		// has rendered its prompt.  We don't require status=="idle" because
		// TUI agents (Claude Code, etc.) keep updating the terminal (cursor
		// blink, status bar) which prevents the watcher from ever settling
		// to "idle" reliably within the timeout window.
		if trustHandled && strings.Contains(output, agent.ReadyStr) {
			return nil
		}
	}

	return fmt.Errorf("agent did not become ready within %v", timeout)
}
