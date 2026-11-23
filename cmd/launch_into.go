package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

var launchTabNew = daemon.DaemonTab
var launchTabShow = daemon.DaemonShowTab
var launchTabSend = daemon.DaemonSendTabSafe

func launchInto(parsed LaunchArgs, agent AgentDef) error {
	cwd := parsed.Cwd
	if !parsed.SharedCwd {
		dir, isolated, err := maybeCreateLaunchWorktree(cwd, parsed.AgentName)
		if err != nil {
			return fmt.Errorf("worktree isolation: %w", err)
		}
		if isolated {
			cwd = dir
			fmt.Fprintf(os.Stderr, "isolated worktree cwd: %s\n", cwd)
		}
	}
	out, err := launchTabNew(parsed.Into, "new-safe", "")
	if err != nil {
		return fmt.Errorf("new tab: %w", err)
	}
	fields := strings.SplitN(out, "|", 2)
	if len(fields) != 2 || fields[1] == "" {
		return fmt.Errorf("invalid new tab response: %s", out)
	}
	tab := "id=" + fields[1]
	fmt.Fprintf(os.Stderr, "session: %s tab: %s %s\n", parsed.Into, fields[0], tab)
	caller := strconv.Itoa(os.Getppid())
	show := func(s, mode, c string) (string, string, error) { return launchTabShow(s, mode, c, tab) }
	deadline := time.Now().Add(shellPromptTimeout)
	shellReady := false
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		screen, _, e := show(parsed.Into, "buffer", caller)
		if e == nil && looksLikeCommandPrompt(screen) {
			shellReady = true
			break
		}
	}
	if !shellReady {
		return fmt.Errorf("shell prompt did not appear in %s; tab left open", tab)
	}
	send := func(msg string) (string, error) { return launchTabSend(parsed.Into, msg, caller, tab) }
	hash := func() (string, error) { b, _, e := show(parsed.Into, "buffer", caller); return daemon.BufHash(b), e }
	launchCmd := fmt.Sprintf("cmd /k \"cd /d %s && %s %s\"", quoteShellArg(cwd), agent.Cmd, quoteShellArgs(agent.Args))
	if _, err := sendWithSubmitRetry(send, hash, launchCmd, 3, 2*time.Second); err != nil {
		return fmt.Errorf("send launch command to %s: %w", tab, err)
	}
	diagnostics, err := waitForReadyUsing(parsed.Into, agent, parsed.ReadyTimeout, show, true)
	if err != nil {
		printLaunchScreen(os.Stderr, parsed.Into, diagnostics)
		return fmt.Errorf("ready wait for %s: %w", tab, err)
	}
	result, err := sendWithSubmitRetry(send, hash, parsed.Prompt, 3, 2*time.Second)
	if err != nil {
		return fmt.Errorf("send prompt to %s: %w", tab, err)
	}
	fmt.Fprintf(os.Stderr, "ready: %s\nsent: %s\n", parsed.AgentName, result)
	fmt.Printf("%s %s\n", parsed.Into, tab)
	return nil
}
