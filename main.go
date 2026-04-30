package main

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/cmd"
	"github.com/YuujiKamura/deckpilot/daemon"
)

var Version = "dev"
var Commit = "unknown"
var BuildTime = "unknown"

func main() {
	cmd.SetVersionVars(Version, Commit, BuildTime)
	daemon.Version = Version
	daemon.Commit = Commit
	daemon.BuildTime = BuildTime
	if len(os.Args) < 2 {
		cmd.List(nil) // default: show sessions
		return
	}
	switch os.Args[1] {
	case "daemon":
		d := daemon.New()
		if err := d.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
			os.Exit(1)
		}
	case "send":
		cmd.Send(os.Args[2:])
	case "list", "ls":
		cmd.List(os.Args[2:])
	case "show":
		cmd.Show(os.Args[2:])
	case "launch":
		cmd.Launch(os.Args[2:])
	case "watch":
		cmd.Watch(os.Args[2:])
	case "hang-detect":
		cmd.HangDetect(os.Args[2:])
	case "auto-approvals", "approve":
		cmd.AutoApprovals(os.Args[2:])
	case "notify":
		cmd.Notify(os.Args[2:])
	case "wait-idle":
		cmd.WaitIdle(os.Args[2:])
	case "shutdown":
		cmd.Shutdown()
	case "version":
		cmd.Version(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(`Usage: deckpilot <command> [args]

Commands:
  send             <session> <message...>         Send message to a session
  show             [session] [--tail N] [--history] [--follow]  Get session buffer (detail, view-only; for approval use auto-approvals)
  list                                            List active sessions (alias: ls)
  launch           <agent> <prompt...> [--cwd D]  Start agent in new Ghostty window
  watch            [session] [--once] [--json]    Monitor sessions (view-only, no approval)
  hang-detect      <session> [--cpu-threshold N]  Non-destructive hang monitor
                   [--stall-seconds N] [--probe-interval DUR]
                   [--on-hang notify|snapshot|ctrl-c|tiered]
                   [--include-children|--no-include-children] [--once]
  auto-approvals   <session> [--interval 2s]      Auto-approve prompts (alias: approve)
                   [--dry-run] [--verbose]
  notify           add|remove|list <args>         Manage idle notification hooks
  wait-idle        <session> [--timeout=D]        Block until session becomes idle (for bg-task push notify)
                   [--poll=D]
  shutdown                                        Stop the daemon process

Run 'deckpilot help' for this message.
`)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\nAvailable commands: send, show, list, ls, launch, watch, auto-approvals, approve, notify, wait-idle, shutdown\nRun 'deckpilot help' for usage.\n", os.Args[1])
		os.Exit(1)
	}
}
