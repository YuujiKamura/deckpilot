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
	if v, bad := daemon.InstanceInvalid(); bad {
		fmt.Fprintf(os.Stderr, "DECKPILOT_INSTANCE=%q is not a valid instance name (lowercase letters, digits, '-', up to 32)\n", v)
		os.Exit(2)
	}
	if len(os.Args) < 2 {
		cmd.List(nil) // default: show sessions
		return
	}
	switch os.Args[1] {
	case "daemon":
		d := daemon.New()
		// Simple flag parsing for daemon
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			if args[i] == "--ws-port" {
				i++
				if i >= len(args) {
					fmt.Fprintf(os.Stderr, "daemon: --ws-port requires a value\n")
					os.Exit(1)
				}
				var port int
				if _, err := fmt.Sscanf(args[i], "%d", &port); err != nil {
					fmt.Fprintf(os.Stderr, "daemon: invalid --ws-port %q: %v\n", args[i], err)
					os.Exit(1)
				}
				d.WSPort = port
				fmt.Printf("daemon: setting WS port to %d\n", port)
			}
		}
		if err := d.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
			os.Exit(1)
		}
	case "send":
		cmd.Send(os.Args[2:])
	case "list", "ls":
		cmd.List(os.Args[2:])
	case "prune":
		cmd.Prune(os.Args[2:])
	case "show":
		cmd.Show(os.Args[2:])
	case "tab":
		cmd.Tab(os.Args[2:])
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
	case "status":
		cmd.Status(os.Args[2:])
	case "cleanup":
		cmd.Cleanup(os.Args[2:])
	case "conduct":
		cmd.Conduct(os.Args[2:])
	case "shutdown":
		cmd.Shutdown()
	case "version":
		cmd.Version(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(`Usage: deckpilot <command> [args]

Commands:
  send             <session> <message...>         Send and report accepted / queued / no-effect
                   [--tab T] (right after session) Type into tab T instead of the active tab
  show             [session] [--tail N] [--history] [--follow]  Get session buffer (detail, view-only; for approval use auto-approvals)
                   [--tab T | --tabs]             Read a background tab (index or id=t_001) / list tabs
  list                                            List active sessions (alias: ls)
  prune            <dead-session>                 Remove tracking for a dead session (does not kill Ghostty)
  tab              new <session> | close <session> <tab>  Create or close a tab
  launch           <agent> <prompt...> [--cwd D] [--into session]  Start agent and wait for idle (use --no-wait to return early)
                   [--no-wait] [--wait-timeout D] [--wait-tail N]
  watch            [session] [--once] [--json]    Monitor sessions (view-only, no approval)
  hang-detect      <session> [--cpu-threshold N]  External non-destructive hang monitor (run alongside daemon)
                   [--stall-seconds N] [--probe-interval DUR]
                   [--on-hang notify|snapshot|ctrl-c|recover|tiered|tiered-recover]
                   [--include-children|--no-include-children] [--once]
  auto-approvals   <session> [--interval 2s]      Auto-approve prompts (alias: approve)
                   [--dry-run] [--verbose]
  notify           add|remove|list|inbox <args>   Manage hooks and read notifications
  wait-idle        <session> [--timeout=D]        Block until session becomes idle (for bg-task push notify)
                   [--poll=D] [--tail N]
  status           <session> [--json]             One line: what the worker is doing, read from files / commits / child processes
  cleanup          [--days N] [--dry-run]         Remove hang dumps older than N days (default 3)
                   [--worktrees]                  + GC dead launch-meta; --worktrees also sweeps orphan worktrees
  conduct          --file PATH [--agent NAME]     Watch a markdown checklist file and auto-launch
                   [--interval 30s] [--one-shot]  agents for each unchecked - [ ] line
  shutdown                                        Stop the daemon process

Run 'deckpilot help' for this message.
`)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\nAvailable commands: send, show, list, ls, prune, launch, watch, hang-detect, auto-approvals, approve, notify, wait-idle, status, cleanup, conduct, shutdown\nRun 'deckpilot help' for usage.\n", os.Args[1])
		os.Exit(1)
	}
}
