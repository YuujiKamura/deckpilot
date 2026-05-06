// Covers: cmd/launch.go (buildAgentLaunchCommand)
// Asserts: agent launch command ends with an unconditional shell-terminator (& exit) so the wrapping cmd.exe does not survive after the agent process exits, preventing ghostty zombie windows
// Why: 2026-05-06 forensic showed 4 ghostty.exe orphans alive after their agents closed, root-caused to cmd shell remaining alive (cmd /C cmd inner shell) because launchCmd had no terminator; daemon stops tracking but window stays as RAM leak invisible to deckpilot list
// BreaksOn: launch.go shell-injection design change (e.g. moving to ghostty --command, switching shell), agent.Cmd containing operators that conflict with `&`

package cmd

import (
	"strings"
	"testing"
)

func TestBuildAgentLaunchCommand_AppendsShellTerminator_PreventsCmdSurvivalAfterAgentExit(t *testing.T) {
	cmd := buildAgentLaunchCommand("C:\\work", "claude", []string{"--model", "sonnet"})

	// The construct sent into an interactive cmd shell must end with `& exit`
	// (NOT `&& exit` — `&&` only fires on success; we need shell to die regardless
	// of agent exit code). Trailing whitespace tolerated.
	trimmed := strings.TrimRight(cmd, " \t\r\n")
	if !strings.HasSuffix(trimmed, "& exit") {
		t.Fatalf("launchCmd must end with `& exit` to terminate the wrapping cmd.exe after agent exits; got: %q", cmd)
	}

	// And `& exit` must NOT be preceded by another `&` (i.e. NOT `&& exit`),
	// because `&&` would skip exit on agent failure → cmd survives → orphan.
	if strings.HasSuffix(trimmed, "&& exit") {
		t.Fatalf("launchCmd uses `&& exit` (success-conditional); orphan returns on agent failure. Use `& exit` for unconditional terminator. got: %q", cmd)
	}
}

func TestBuildAgentLaunchCommand_PreservesCdAndAgentInvocation(t *testing.T) {
	cmd := buildAgentLaunchCommand("C:\\work\\repo", "codex", []string{"--full-auto"})

	if !strings.Contains(cmd, `cd /d "C:\work\repo"`) {
		t.Errorf("launchCmd must include `cd /d \"<cwd>\"` for the working directory; got: %q", cmd)
	}
	if !strings.Contains(cmd, "codex --full-auto") {
		t.Errorf("launchCmd must include the agent invocation; got: %q", cmd)
	}
}

// Summary: pins the contract that buildAgentLaunchCommand always produces
// a chain ending in `& exit`, so the wrapping cmd.exe cannot survive past
// agent exit and create an orphan ghostty window invisible to deckpilot
// list. Failures = orphan ghostty regression returns or builder behavior
// drifted (cd missing, agent invocation missing).
