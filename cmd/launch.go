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

// LaunchArgs is the parsed result of `deckpilot launch ...` flags. The
// fields are deliberately concrete (not a flag.FlagSet) so tests can
// exercise the parser without poking at the global flag state and so
// the calling Launch can keep its existing fmt.Fprintln error
// reporting style.
type LaunchArgs struct {
	AgentName    string
	Prompt       string
	Cwd          string
	NoMetaPrompt bool
	SharedCwd    bool
}

// ParseLaunchArgs is the args parser carved out of Launch so tests can
// pin the --no-meta-prompt / --cwd / prompt-collection rules without
// going through os.Exit. Returns (parsed, "") on success, or
// (zero, error message) on a usage error. The caller decides whether
// to write the error to stderr and exit.
//
// args is the slice after the subcommand name (i.e. `launch <agent>
// <prompt...>` minus the `launch` token).
func ParseLaunchArgs(args []string, defaultCwd string) (LaunchArgs, string) {
	if len(args) < 1 {
		return LaunchArgs{}, "usage: deckpilot launch <agent> <prompt...> [--cwd DIR] [--shared-cwd] [--no-meta-prompt]"
	}
	out := LaunchArgs{
		AgentName: args[0],
		Cwd:       defaultCwd,
	}
	var promptParts []string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--cwd":
			if i+1 >= len(args) {
				return LaunchArgs{}, "--cwd requires a value"
			}
			out.Cwd = args[i+1]
			i++
		case "--no-meta-prompt":
			out.NoMetaPrompt = true
		case "--shared-cwd":
			out.SharedCwd = true
		default:
			promptParts = append(promptParts, args[i])
		}
	}
	out.Prompt = strings.Join(promptParts, " ")
	if out.Prompt == "" {
		return LaunchArgs{}, "usage: deckpilot launch <agent> <prompt...> [--cwd DIR] [--shared-cwd] [--no-meta-prompt]"
	}
	return out, ""
}

// Launch starts a Ghostty window running the specified agent, waits for the
// session to appear, handles trust confirmation, waits for ready, and sends
// the prompt. Prints the session name on success.
func Launch(args []string) {
	cwdDefault, _ := os.Getwd()
	parsed, errMsg := ParseLaunchArgs(args, cwdDefault)
	if errMsg != "" {
		fmt.Fprintln(os.Stderr, errMsg)
		os.Exit(1)
	}

	agentName := parsed.AgentName
	agent, ok := agents[agentName]
	if !ok {
		names := make([]string, 0, len(agents))
		for k := range agents {
			names = append(names, k)
		}
		fmt.Fprintf(os.Stderr, "unknown agent: %s (available: %s)\n", agentName, strings.Join(names, ", "))
		os.Exit(1)
	}

	cwd := parsed.Cwd
	prompt := parsed.Prompt
	noMetaPrompt := parsed.NoMetaPrompt

	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}

	// Snapshot current sessions
	knownSessions := listSessionNames()

	// Find and launch Ghostty
	ghosttyExe := FindGhostty()
	if ghosttyExe == "" {
		fmt.Fprintln(os.Stderr, "ghostty not found. Set GHOSTTY_EXE or install ghostty on PATH")
		os.Exit(1)
	}

	if !parsed.SharedCwd {
		isolatedCwd, isolated, err := maybeCreateLaunchWorktree(cwd, agentName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "worktree isolation: %v\n", err)
			os.Exit(1)
		}
		if isolated {
			cwd = isolatedCwd
			fmt.Fprintf(os.Stderr, "isolated worktree cwd: %s\n", cwd)
		}
	}

	// Launch plain Ghostty, then send commands via CP after session is up.
	// Using -e causes IO thread errors on debug builds, so we launch bare
	// and type the command into the shell.
	// We use explorer.exe to spawn ghostty because CREATE_BREAKAWAY_FROM_JOB
	// fails if the current job object has strict limits (e.g. from antigravity-cli).
	// explorer.exe delegates the launch to the main Explorer process (DCOM),
	// completely escaping the job object.
	cmd := exec.Command("explorer.exe", ghosttyExe)
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

	// Persist launch metadata so the hang-detect snapshot action can
	// emit an exact `deckpilot launch ... --cwd ...` resume command if
	// this session ever stops responding. Best-effort: a write failure
	// here would only cost the resume hint, not the launch itself.
	if metaErr := WriteLaunchMeta(LaunchMeta{
		SessionName: sessionName,
		Agent:       agentName,
		Cwd:         cwd,
		Prompt:      prompt,
		Redacted:    noMetaPrompt,
	}); metaErr != nil {
		fmt.Fprintf(os.Stderr, "launch-meta: %v (continuing)\n", metaErr)
	}

	// Wait for shell prompt (give the terminal a moment to render)
	time.Sleep(2 * time.Second)

	// Send cd + agent launch command.
	// Use cmd /k so that the shell does not exit when the agent exits autonomously,
	// keeping the Ghostty window open for log inspection.
	launchCmd := fmt.Sprintf("cmd /k \"cd /d \"%s\" && %s %s\"",
		cwd,
		agent.Cmd,
		quoteShellArgs(agent.Args))
	caller := strconv.Itoa(os.Getppid())
	_, err = daemonSendWithRetry(sessionName, launchCmd, caller, 3, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send launch cmd: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "sent launch: %s %s\n", agent.Cmd, strings.Join(agent.Args, " "))

	// Wait for trust or ready
	err = waitForReady(sessionName, agent, 60*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ready wait: %v (leaving Ghostty for debugging)\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "ready: %s\n", agentName)

	// Send prompt
	result, err := daemonSendWithRetry(sessionName, prompt, caller, 3, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "sent: %s\n", result)

	// Print session name to stdout for scripting
	fmt.Println(sessionName)
}

func quoteShellArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quoteShellArg(arg))
	}
	return strings.Join(quoted, " ")
}

func quoteShellArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if strings.IndexFunc(arg, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '"' || r == '&' || r == '|' || r == '<' || r == '>' || r == '^'
	}) == -1 {
		return arg
	}
	return `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
}

func maybeCreateLaunchWorktree(cwd, agentName string) (string, bool, error) {
	repoRoot, err := gitOutput(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return cwd, false, nil
	}
	repoRoot = filepath.Clean(repoRoot)

	if isDeckpilotManagedWorktree(repoRoot) {
		return cwd, false, nil
	}

	prefix, err := gitOutput(cwd, "rev-parse", "--show-prefix")
	if err != nil {
		return cwd, false, fmt.Errorf("git rev-parse --show-prefix: %w", err)
	}

	worktreeRoot := managedWorktreesDir()
	if err := os.MkdirAll(worktreeRoot, 0o700); err != nil {
		return cwd, false, fmt.Errorf("mkdir %s: %w", worktreeRoot, err)
	}

	worktreePath := filepath.Join(worktreeRoot, fmt.Sprintf("%s-%s-%d",
		filepath.Base(repoRoot), agentName, time.Now().UnixNano()))
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "--detach", worktreePath, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return cwd, false, fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}

	launchCwd := filepath.Join(worktreePath, filepath.FromSlash(strings.TrimSpace(prefix)))
	if st, err := os.Stat(launchCwd); err != nil || !st.IsDir() {
		launchCwd = worktreePath
	}
	return launchCwd, true, nil
}

func gitOutput(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func isDeckpilotManagedWorktree(path string) bool {
	rel, err := filepath.Rel(managedWorktreesDir(), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func FindGhostty() string {
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
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)

		output, _, err := daemon.DaemonShow(session, "buffer", caller)
		if err != nil {
			continue
		}

		// Handle trust confirmation. Detect fuzzily by the dialog's stable
		// structure (topic "trust" + a confirm affordance), NOT an exact
		// phrase — claude changed "trust the contents" → "trust this folder"
		// once already (2026-06-04) and an exact match silently stopped
		// firing, sending the prompt into the unanswered menu. TrustStr
		// non-empty just opts the agent into the check.
		if looksLikeTrustDialog(output) {
			// Send Enter to accept trust ("Yes, I trust" is the default).
			// Do NOT gate readiness on having seen a dialog: most folders are
			// already trusted and never show one — gating hung the launch
			// forever waiting for a dialog that never came (2026-06-04
			// regression: claude was ready, launch killed it at the timeout).
			daemon.DaemonSend(session, "", caller)
			time.Sleep(2 * time.Second)
			continue
		}

		// Check for ready  Eonce the ready string is visible, the agent
		// has rendered its prompt.  We don't require status=="idle" because
		// TUI agents (Claude Code, etc.) keep updating the terminal (cursor
		// blink, status bar) which prevents the watcher from ever settling
		// to "idle" reliably within the timeout window.
		if strings.Contains(output, agent.ReadyStr) {
			return nil
		}
	}

	return fmt.Errorf("agent did not become ready within %v", timeout)
}

// looksLikeTrustDialog fuzzily detects a folder-trust / workspace-trust
// confirmation prompt in a TUI buffer, tolerant of wording changes across
// agent versions. It requires two co-signals so normal output mentioning
// "trust" in passing does not trip it:
//
//  1. the topic — the word "trust" (case-insensitive), and
//  2. a confirmation affordance — the prompt is asking to confirm/cancel.
//
// Both claude ("Quick safety check ... Yes, I trust this folder ... Enter to
// confirm · Esc to cancel") and codex ("Do you trust the authors ... (trust)")
// satisfy this without hardcoding either exact phrase.
func looksLikeTrustDialog(buf string) bool {
	lo := strings.ToLower(buf)
	if !strings.Contains(lo, "trust") {
		return false
	}
	for _, affordance := range []string{
		"confirm",        // "Enter to confirm"
		"esc to cancel",  // claude trust menu footer
		"yes, i trust",   // claude option 1
		"do you trust",   // codex / generic phrasing
		"(y/n)", "[y/n]", // terse yes/no prompts
	} {
		if strings.Contains(lo, affordance) {
			return true
		}
	}
	return false
}
