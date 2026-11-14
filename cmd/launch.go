package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	AgentName       string
	Prompt          string
	Cwd             string
	ReadyTimeout    time.Duration
	ReadyTimeoutSet bool
	NoMetaPrompt    bool
	SharedCwd       bool
}

const (
	defaultReadyTimeout = 2 * time.Minute
	shellPromptTimeout  = 30 * time.Second
)

type readyWaitDiagnostics struct {
	Samples        int
	ChangedSamples int
	SawPromptLike  bool
	ReadyMatched   bool
	LastStatus     string
	LastBuffer     string
	LastShowError  string
}

type launchTrace struct {
	StartedAt     time.Time
	Session       string
	Agent         string
	WindowStart   time.Duration
	SessionDetect time.Duration
	ShellPrepare  time.Duration
	CommandSend   time.Duration
	ReadyWait     time.Duration
	PromptSend    time.Duration
	ShellReady    bool
	CommandSent   bool
	ReadyMatched  bool
	ReadyMarker   string
	PromptSent    bool
	FailureStage  string
	Diagnostics   readyWaitDiagnostics
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
		return LaunchArgs{}, "usage: deckpilot launch <agent> <prompt...> [--cwd DIR] [--ready-timeout DURATION] [--shared-cwd] [--no-meta-prompt]"
	}
	out := LaunchArgs{
		AgentName:    args[0],
		Cwd:          defaultCwd,
		ReadyTimeout: defaultReadyTimeout,
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
		case "--ready-timeout":
			if i+1 >= len(args) {
				return LaunchArgs{}, "--ready-timeout requires a duration (for example 2m)"
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				return LaunchArgs{}, "--ready-timeout must be a positive duration (for example 2m)"
			}
			out.ReadyTimeout = d
			out.ReadyTimeoutSet = true
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
		return LaunchArgs{}, "usage: deckpilot launch <agent> <prompt...> [--cwd DIR] [--ready-timeout DURATION] [--shared-cwd] [--no-meta-prompt]"
	}
	return out, ""
}

// Launch starts a Ghostty window running the specified agent, waits for the
// session to appear, handles trust confirmation, waits for ready, and sends
// the prompt. Prints the session name on success.
func Launch(args []string) {
	if err := LaunchWithError(args); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func LaunchWithError(args []string) (retErr error) {
	cwdDefault, _ := os.Getwd()
	parsed, errMsg := ParseLaunchArgs(args, cwdDefault)
	if errMsg != "" {
		return fmt.Errorf("%s", errMsg)
	}
	if !parsed.ReadyTimeoutSet {
		if value := os.Getenv("DECKPILOT_READY_TIMEOUT"); value != "" {
			d, parseErr := time.ParseDuration(value)
			if parseErr != nil || d <= 0 {
				return fmt.Errorf("DECKPILOT_READY_TIMEOUT must be a positive duration (for example 2m)")
			}
			parsed.ReadyTimeout = d
		}
	}

	agentName := parsed.AgentName
	agent, ok := agents[agentName]
	if !ok {
		// Not in the table: run it as a command line as given. deckpilot
		// needs no per-CLI knowledge for this path (see genericAgent).
		agent, ok = genericAgent(agentName)
		if !ok {
			return fmt.Errorf("empty agent command")
		}
	}
	if err := checkAgentCLI(agent); err != nil {
		return fmt.Errorf("agent CLI check: %w", err)
	}

	cwd := parsed.Cwd
	prompt := parsed.Prompt
	noMetaPrompt := parsed.NoMetaPrompt

	if err := daemon.EnsureRunning(); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}

	// Snapshot current sessions
	knownSessions, err := listSessionNames()
	if err != nil {
		return fmt.Errorf("session snapshot: %w", err)
	}

	// Find and launch Ghostty
	ghosttyExe := FindGhostty()
	if ghosttyExe == "" {
		return fmt.Errorf("ghostty not found. Set GHOSTTY_EXE or install ghostty on PATH")
	}

	if !parsed.SharedCwd {
		isolatedCwd, isolated, err := maybeCreateLaunchWorktree(cwd, agentName)
		if err != nil {
			return fmt.Errorf("worktree isolation: %w", err)
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
	trace := launchTrace{StartedAt: deckpilotNow(), Agent: agentName, ReadyMarker: agent.ReadyStr}
	stageStarted := time.Now()
	cmd := exec.Command("explorer.exe", ghosttyExe)
	cmd.Dir = cwd
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS = 0x00000008
	}
	if err := cmd.Start(); err != nil {
		trace.WindowStart = time.Since(stageStarted)
		trace.FailureStage = "window_start"
		if path, saveErr := writeLaunchRecord(trace, err); saveErr != nil {
			return fmt.Errorf("launch ghostty: %w (saving launch record failed: %v)", err, saveErr)
		} else {
			return fmt.Errorf("launch ghostty: %w (launch record: %s)", err, path)
		}
	}
	trace.WindowStart = time.Since(stageStarted)

	// Once started, completely detach. Do not Wait(), do not keep handles.
	// The daemon will pick up the session via file scanning.
	if cmd.Process != nil {
		cmd.Process.Release()
	}
	fmt.Fprintf(os.Stderr, "launched: %s\n", ghosttyExe)

	// Wait for new session to appear
	trace.FailureStage = "session_detect"
	stageStarted = time.Now()
	sessionName, err := waitForNewSession(knownSessions, 15*time.Second)
	trace.SessionDetect = time.Since(stageStarted)
	if err != nil {
		trace.FailureStage = "session_detect"
		if path, saveErr := writeLaunchRecord(trace, err); saveErr != nil {
			return fmt.Errorf("session detect: %w (saving launch record failed: %v)", err, saveErr)
		} else {
			return fmt.Errorf("session detect: %w (launch record: %s)", err, path)
		}
	}
	trace.Session = sessionName
	fmt.Fprintf(os.Stderr, "session: %s\n", sessionName)
	launchSucceeded := false
	defer func() {
		// A launch that stopped on a screen only a person can answer keeps
		// its window: closing it would throw away the very thing the caller
		// has to look at, and make "session left open" a lie.
		if !launchSucceeded && !errors.Is(retErr, errSessionLeftOpen) {
			killLaunchedGhostty(sessionName, knownSessions, ghosttyExe)
		}
	}()
	defer func() {
		path, saveErr := writeLaunchRecord(trace, retErr)
		if saveErr != nil {
			if retErr == nil {
				retErr = fmt.Errorf("launch succeeded but saving launch record failed: %w", saveErr)
			} else {
				retErr = fmt.Errorf("%w (saving launch record failed: %v)", retErr, saveErr)
			}
			return
		}
		if retErr != nil {
			retErr = fmt.Errorf("%w (launch record: %s)", retErr, path)
		}
	}()

	// Persist launch metadata so the hang-detect snapshot action can
	// emit an exact `deckpilot launch ... --cwd ...` resume command if
	// this session ever stops responding. Best-effort: a write failure
	// here would only cost the resume hint, not the launch itself.
	launchMeta := LaunchMeta{
		SessionName: sessionName,
		Agent:       agentName,
		Cwd:         cwd,
		Prompt:      prompt,
		Redacted:    noMetaPrompt,
	}
	if metaErr := WriteLaunchMeta(launchMeta); metaErr != nil {
		fmt.Fprintf(os.Stderr, "launch-meta: %v (continuing)\n", metaErr)
	}

	// Wait for cmd.exe to render its prompt. A fixed delay races slow
	// Ghostty startup and can lose the first command's Enter.
	trace.FailureStage = "shell_prepare"
	stageStarted = time.Now()
	diagnostics, waitErr := waitForShellPrompt(sessionName, shellPromptTimeout)
	trace.ShellPrepare = time.Since(stageStarted)
	trace.Diagnostics = diagnostics
	if waitErr != nil {
		return fmt.Errorf("shell wait: %w", waitErr)
	}
	trace.ShellReady = true

	// Send cd + agent launch command.
	// Use cmd /k so that the shell does not exit when the agent exits autonomously,
	// keeping the Ghostty window open for log inspection.
	launchCmd := fmt.Sprintf("cmd /k \"cd /d %s && %s %s\"",
		quoteShellArg(cwd),
		agent.Cmd,
		quoteShellArgs(agent.Args))
	caller := strconv.Itoa(os.Getppid())
	trace.FailureStage = "command_send"
	stageStarted = time.Now()
	_, err = daemonSendWithRetry(sessionName, launchCmd, caller, 3, 2*time.Second)
	trace.CommandSend = time.Since(stageStarted)
	if err != nil {
		trace.Diagnostics, _ = captureLaunchDiagnostics(sessionName)
		return fmt.Errorf("send launch cmd: %w", err)
	}
	trace.CommandSent = true
	launchMeta.AgentStarted = true
	if metaErr := WriteLaunchMeta(launchMeta); metaErr != nil {
		fmt.Fprintln(os.Stderr, "launch-meta:", metaErr, "(continuing)")
	}
	fmt.Fprintf(os.Stderr, "sent launch: %s %s\n", agent.Cmd, strings.Join(agent.Args, " "))

	// Wait for trust or ready
	trace.FailureStage = "ready_wait"
	stageStarted = time.Now()
	diagnostics, waitErr = waitForReady(sessionName, agent, parsed.ReadyTimeout)
	trace.ReadyWait = time.Since(stageStarted)
	trace.Diagnostics = diagnostics
	trace.ReadyMatched = diagnostics.ReadyMatched
	err = waitErr
	if err != nil {
		// Hand the caller what is actually on the screen. deckpilot does
		// not interpret or answer it; whoever launched decides.
		printLaunchScreen(os.Stderr, sessionName, diagnostics)
		if errors.Is(err, errSessionLeftOpen) {
			fmt.Fprintln(os.Stderr, "the prompt was NOT sent. Answer the screen in the window, then run: deckpilot send "+sessionName+" <prompt>")
		}
		return fmt.Errorf("ready wait: %w", err)
	}
	fmt.Fprintf(os.Stderr, "ready: %s\n", agentName)

	// Send prompt
	trace.FailureStage = "prompt_send"
	stageStarted = time.Now()
	result, err := daemonSendWithRetry(sessionName, prompt, caller, 3, 2*time.Second)
	trace.PromptSend = time.Since(stageStarted)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	trace.PromptSent = true
	trace.FailureStage = "complete"
	if agent.Generic {
		// deckpilot judged "ready" only from the screen having settled.
		// Show what the prompt landed on so the caller can check.
		time.Sleep(2 * readyPollInterval)
		if out, status, showErr := daemonShowFn(sessionName, "buffer", caller); showErr == nil {
			fmt.Fprintln(os.Stderr, "deckpilot has no knowledge of this CLI; the prompt was typed once the screen stopped changing. Check it:")
			printLaunchScreen(os.Stderr, sessionName, readyWaitDiagnostics{LastBuffer: out, LastStatus: status})
		}
	}
	fmt.Fprintf(os.Stderr, "sent: %s\n", result)
	launchSucceeded = true

	// Print session name to stdout for scripting
	fmt.Println(sessionName)
	return nil
}

func checkAgentCLI(agent AgentDef) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if agent.Generic {
		// An unknown CLI may not have --help; only require that it exists.
		if _, err := exec.LookPath(agent.Cmd); err != nil {
			return fmt.Errorf("%s is not on PATH: %w", agent.Cmd, err)
		}
		return nil
	}
	parts := append(append([]string(nil), agent.Args...), "--help")
	command := "& " + quotePowerShellArg(agent.Cmd)
	for _, arg := range parts {
		command += " " + quotePowerShellArg(arg)
	}
	command += "; if ($null -eq $LASTEXITCODE) { exit 1 }; exit $LASTEXITCODE"
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s help timed out after 8s", agent.Cmd)
		}
		return fmt.Errorf("%s rejected configured launch arguments: %w: %s", agent.Cmd, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func quotePowerShellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
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

func listSessionNames() ([]string, error) {
	raw, err := daemon.DaemonList()
	if err != nil {
		return nil, err
	}
	var sessions []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		return nil, fmt.Errorf("decode session list: %w", err)
	}
	names := make([]string, len(sessions))
	for i, s := range sessions {
		names[i] = s.Name
	}
	return names, nil
}

func newSessionNames(known map[string]bool, current []string) []string {
	var added []string
	for _, name := range current {
		if !known[name] {
			added = append(added, name)
		}
	}
	return added
}

func waitForNewSession(known []string, timeout time.Duration) (string, error) {
	knownSet := make(map[string]bool, len(known))
	for _, n := range known {
		knownSet[n] = true
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		current, err := listSessionNames()
		if err != nil {
			lastErr = err
			continue
		}
		added := newSessionNames(knownSet, current)
		if len(added) > 1 {
			return "", fmt.Errorf("multiple new sessions detected; refusing ambiguous launch ownership: %s", strings.Join(added, ", "))
		}
		if len(added) == 1 {
			return added[0], nil
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("no new session detected within %v; last list error: %w", timeout, lastErr)
	}
	return "", fmt.Errorf("no new session detected within %v", timeout)
}

func captureLaunchDiagnostics(session string) (readyWaitDiagnostics, error) {
	output, status, err := daemon.DaemonShow(session, "buffer", strconv.Itoa(os.Getppid()))
	diagnostics := readyWaitDiagnostics{}
	if err != nil {
		diagnostics.LastShowError = err.Error()
		return diagnostics, err
	}
	diagnostics.LastBuffer = output
	diagnostics.LastStatus = status
	diagnostics.SawPromptLike = containsPromptLikeLine(output)
	diagnostics.Samples = 1
	return diagnostics, nil
}

func waitForShellPrompt(session string, timeout time.Duration) (readyWaitDiagnostics, error) {
	caller := strconv.Itoa(os.Getppid())
	deadline := time.Now().Add(timeout)
	diagnostics := readyWaitDiagnostics{}
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		output, status, err := daemon.DaemonShow(session, "buffer", caller)
		if err != nil {
			diagnostics.LastShowError = err.Error()
			continue
		}
		diagnostics.LastShowError = ""
		diagnostics.Samples++
		diagnostics.LastBuffer = output
		diagnostics.LastStatus = status
		diagnostics.SawPromptLike = diagnostics.SawPromptLike || containsPromptLikeLine(output)
		if looksLikeCommandPrompt(output) {
			return diagnostics, nil
		}
	}
	return diagnostics, fmt.Errorf("shell command prompt did not appear within %v", timeout)
}

// killLaunchedGhostty terminates only the newly detected Ghostty session PID
// after matching the daemon's name/PID pair and verifying the executable path.
// explorer.exe's PID is not the Ghostty PID, so it must never be killed here.
func killLaunchedGhostty(session string, known []string, ghosttyExe string) {
	entries, err := readSessionEntries()
	if err != nil {
		return
	}
	pid, ok := ownedGhosttyPID(session, known, entries)
	if !ok {
		return
	}
	pidText := strconv.Itoa(pid)
	command := "$p=Get-CimInstance Win32_Process -Filter 'ProcessId = " + pidText + "'; if ($p -and [IO.Path]::GetFullPath($p.ExecutablePath) -ieq [IO.Path]::GetFullPath(" + quotePowerShellArg(ghosttyExe) + ")) { Stop-Process -Id " + pidText + " -Force }"
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "launch cleanup: could not terminate owned Ghostty session %s: %v: %s\n", session, err, strings.TrimSpace(string(output)))
	}
}

func ownedGhosttyPID(session string, known []string, entries []listEntry) (int, bool) {
	if session == "" || !strings.HasPrefix(session, "ghostty-") {
		return 0, false
	}
	for _, name := range known {
		if name == session {
			return 0, false
		}
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(session, "ghostty-"))
	if err != nil || pid <= 0 {
		return 0, false
	}
	for _, entry := range entries {
		if entry.Name == session && entry.PID == pid {
			return pid, true
		}
	}
	return 0, false
}

func readSessionEntries() ([]listEntry, error) {
	raw, err := daemon.DaemonList()
	if err != nil {
		return nil, err
	}
	var entries []listEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func waitForReady(session string, agent AgentDef, timeout time.Duration) (readyWaitDiagnostics, error) {
	caller := strconv.Itoa(os.Getppid())
	deadline := time.Now().Add(timeout)
	trustAccepted := false
	trustMoveCount := 0
	trustMoveChecks := 0
	trustDismissChecks := 0
	diagnostics := readyWaitDiagnostics{}
	previousHash := ""
	commandPromptSamples := 0
	showFailures := 0
	menuSamples := 0
	settledSamples := 0

	for time.Now().Before(deadline) {
		time.Sleep(readyPollInterval)

		output, status, err := daemonShowFn(session, "buffer", caller)
		if err == nil && strings.HasPrefix(status, "stale:") {
			err = fmt.Errorf("screen is stale (%s)", status)
		}
		if err != nil {
			diagnostics.LastShowError = err.Error()
			showFailures++
			// Not being able to see the screen is not "still starting".
			// Report blindness as soon as it is clearly not a blip.
			if showFailures >= maxBlindSamples {
				return diagnostics, fmt.Errorf("cannot read the session screen (%d consecutive failures): %w", showFailures, err)
			}
			continue
		}
		showFailures = 0
		diagnostics.LastShowError = ""
		diagnostics.Samples++
		diagnostics.LastBuffer = output
		diagnostics.LastStatus = status
		diagnostics.SawPromptLike = diagnostics.SawPromptLike || containsPromptLikeLine(output)
		hash := daemon.BufHash(output)
		if previousHash != "" && hash != previousHash {
			diagnostics.ChangedSamples++
			settledSamples = 0
		} else if previousHash != "" {
			settledSamples++
		}
		previousHash = hash
		if looksLikeCommandPrompt(output) {
			commandPromptSamples++
			if commandPromptSamples >= 3 {
				return diagnostics, fmt.Errorf("agent exited (command prompt returned before ready)")
			}
		} else {
			commandPromptSamples = 0
		}

		// Handle trust confirmation. Detect fuzzily by the dialog's stable
		// structure (topic "trust" + a confirm affordance), NOT an exact
		// phrase — claude changed "trust the contents" → "trust this folder"
		// once already (2026-06-04) and an exact match silently stopped
		// firing, sending the prompt into the unanswered menu. TrustStr
		// non-empty just opts the agent into the check.
		if looksLikeTrustDialog(output) {
			if trustAccepted {
				trustDismissChecks++
				if trustDismissChecks >= 5 {
					return diagnostics, fmt.Errorf("trust confirmation was sent once but the dialog did not close: %w", errSessionLeftOpen)
				}
				continue // wait for the one confirmation to dismiss; never repeat Enter
			}
			key, ready := trustDialogKey(output)
			if key != "" && trustMoveCount < 1 {
				if err := daemon.DaemonSendRaw(session, []byte(key)); err != nil {
					return diagnostics, fmt.Errorf("trust dialog navigation failed: %w", err)
				}
				trustMoveCount++
				continue
			}
			if key != "" && trustMoveCount == 1 {
				trustMoveChecks++
				if trustMoveChecks < 5 {
					continue // allow the TUI to redraw; never repeat the navigation key
				}
				return diagnostics, fmt.Errorf("trust option did not become selected after navigation: %w", errSessionLeftOpen)
			}
			if ready {
				if _, err := daemon.DaemonSend(session, "", caller); err != nil {
					return diagnostics, fmt.Errorf("trust confirmation failed: %w", err)
				}
				trustAccepted = true
				continue
			}
			return diagnostics, fmt.Errorf("trust dialog found but cannot identify a selected trust option: %w", errSessionLeftOpen)
		}
		if trustAccepted && looksLikeCommandPrompt(output) {
			return diagnostics, fmt.Errorf("Claude exited after trust confirmation and returned to the command prompt: %w", errSessionLeftOpen)
		}
		if trustAccepted {
			trustDismissChecks = 0
		}
		// Check for ready  Eonce the ready string is visible, the agent
		// has rendered its prompt.  We don't require status=="idle" because
		// TUI agents (Claude Code, etc.) keep updating the terminal (cursor
		// blink, status bar) which prevents the watcher from ever settling
		// to "idle" reliably within the timeout window.
		// A menu waits for a choice; its selection cursor can be the very
		// character used as the ready marker. deckpilot does not pick an
		// option: it stops and shows the screen to whoever launched.
		if looksLikeMenu(output) {
			menuSamples++
			if menuSamples >= menuSamplesBeforeReport {
				return diagnostics, fmt.Errorf("agent is showing a menu and waits for a choice (not answered by deckpilot): %w", errSessionLeftOpen)
			}
			continue
		}
		menuSamples = 0
		if agent.ReadyStr == "" {
			// No per-CLI marker: ready means the program drew something
			// after the launch command and then stopped changing, and it
			// is neither a menu nor the shell prompt.
			if drewAfterLaunch(output) && settledSamples >= settleSamplesForReady && !looksLikeCommandPrompt(output) {
				diagnostics.ReadyMatched = true
				return diagnostics, nil
			}
			continue
		}
		if strings.Contains(output, agent.ReadyStr) {
			diagnostics.ReadyMatched = true
			return diagnostics, nil
		}
	}

	if diagnostics.Samples > 0 && diagnostics.ChangedSamples == 0 {
		return diagnostics, fmt.Errorf("agent is waiting on a screen that did not change for %v and is not the ready prompt", timeout)
	}
	return diagnostics, fmt.Errorf("agent did not become ready within %v", timeout)
}

var commandPromptLine = regexp.MustCompile(`^\s*[A-Za-z]:\\[^\r\n]*>\s*$`)

func looksLikeCommandPrompt(buf string) bool {
	lines := strings.Split(buf, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return commandPromptLine.MatchString(line)
	}
	return false
}

func containsPromptLikeLine(buf string) bool {
	for _, line := range strings.Split(buf, "\n") {
		line = strings.TrimSpace(line)
		if line == "❯" || line == "›" || line == ">" || line == "$" || line == "%" {
			return true
		}
	}
	return false
}

func writeLaunchFailure(session string, cause error, diagnostics readyWaitDiagnostics) (string, error) {
	if diagnostics.LastBuffer == "" {
		buffer, status, err := daemon.DaemonShow(session, "buffer", strconv.Itoa(os.Getppid()))
		if err != nil {
			diagnostics.LastShowError = err.Error()
		} else {
			diagnostics.LastBuffer = buffer
			diagnostics.LastStatus = status
		}
	}
	if err := os.MkdirAll(launchFailDir(), 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.txt", sanitizeFilename(session), deckpilotNow().Format("20060102T150405.000000000"))
	path := filepath.Join(launchFailDir(), name)
	var body strings.Builder
	fmt.Fprintf(&body, "session: %s\nreason: %v\nsamples: %d\nchanged_samples: %d\nprompt_like_seen: %t\nlast_status: %s\nlast_show_error: %s\n\n--- last buffer ---\n",
		session, cause, diagnostics.Samples, diagnostics.ChangedSamples, diagnostics.SawPromptLike, diagnostics.LastStatus, diagnostics.LastShowError)
	body.WriteString(trimScreen(diagnostics.LastBuffer))
	if !strings.HasSuffix(diagnostics.LastBuffer, "\n") {
		body.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func writeLaunchRecord(trace launchTrace, outcome error) (string, error) {
	if outcome != nil && trace.Session != "" && trace.Diagnostics.LastBuffer == "" {
		if diagnostics, err := captureLaunchDiagnostics(trace.Session); err == nil {
			trace.Diagnostics = diagnostics
		}
	}
	if err := os.MkdirAll(launchFailDir(), 0o700); err != nil {
		return "", err
	}
	stamp := trace.StartedAt
	if stamp.IsZero() {
		stamp = deckpilotNow()
	}
	base := sanitizeFilename(trace.Session)
	if trace.Session == "" {
		base = "launch"
	}
	path := filepath.Join(launchFailDir(), fmt.Sprintf("%s-%s.txt", base, stamp.Format("20060102T150405.000000000")))
	status := "success"
	reason := ""
	if outcome != nil {
		status = "failure"
		reason = outcome.Error()
	}
	var body strings.Builder
	fmt.Fprintf(&body, "session: %s\nagent: %s\noutcome: %s\nfailure_stage: %s\nreason: %s\nelapsed: %s\n", trace.Session, trace.Agent, status, trace.FailureStage, reason, deckpilotNow().Sub(stamp).Round(time.Millisecond))
	fmt.Fprintf(&body, "window_start_elapsed: %s\nsession_detect_elapsed: %s\nshell_prepare_elapsed: %s\ncommand_send_elapsed: %s\nready_wait_elapsed: %s\nprompt_send_elapsed: %s\n", trace.WindowStart, trace.SessionDetect, trace.ShellPrepare, trace.CommandSend, trace.ReadyWait, trace.PromptSend)
	fmt.Fprintf(&body, "shell_ready: %t\ncommand_sent: %t\nready_marker: %s\nready_matched: %t\nprompt_sent: %t\nsamples: %d\nchanged_samples: %d\nprompt_like_seen: %t\nlast_status: %s\nlast_show_error: %s\n", trace.ShellReady, trace.CommandSent, trace.ReadyMarker, trace.ReadyMatched, trace.PromptSent, trace.Diagnostics.Samples, trace.Diagnostics.ChangedSamples, trace.Diagnostics.SawPromptLike, trace.Diagnostics.LastStatus, trace.Diagnostics.LastShowError)
	if outcome != nil {
		body.WriteString("\n--- last buffer ---\n")
		body.WriteString(trimScreen(trace.Diagnostics.LastBuffer))
		if !strings.HasSuffix(trace.Diagnostics.LastBuffer, "\n") {
			body.WriteByte('\n')
		}
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// trustDialogKey chooses only from an explicitly rendered selection. A
// positive choice is confirmed only after the buffer shows it selected.
func trustDialogKey(buf string) (navigate string, trustSelected bool) {
	lines := strings.Split(buf, "\n")
	for i, line := range lines {
		choice := selectedTrustChoice(line)
		if choice == "" {
			continue
		}
		if isPositiveTrustChoice(choice) {
			return "", true
		}
		if !strings.Contains(choice, "no") && !strings.Contains(choice, "exit") {
			continue
		}
		if i+1 < len(lines) && isPositiveTrustChoice(strings.ToLower(strings.TrimSpace(lines[i+1]))) {
			return "\x1b[B", false
		}
		if i > 0 && isPositiveTrustChoice(strings.ToLower(strings.TrimSpace(lines[i-1]))) {
			return "\x1b[A", false
		}
		return "", false
	}
	return "", false
}

func selectedTrustChoice(line string) string {
	trimmed := strings.TrimSpace(line)
	// agy (Antigravity CLI) marks the selected row with a plain ASCII ">".
	if !strings.HasPrefix(trimmed, "❯") && !strings.HasPrefix(trimmed, "›") && !strings.HasPrefix(trimmed, "> ") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "❯›> ")))
}

func isPositiveTrustChoice(choice string) bool {
	return strings.Contains(choice, "trust") || strings.Contains(choice, "yes")
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
