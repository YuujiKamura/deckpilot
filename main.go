package main

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/cmd"
	"github.com/YuujiKamura/deckpilot/daemon"
)

func main() {
	if len(os.Args) < 2 {
		cmd.List() // default: show sessions
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
		cmd.List()
	case "show":
		cmd.Show(os.Args[2:])
	case "launch":
		cmd.Launch(os.Args[2:])
	case "watch":
		cmd.Watch(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(`Usage: deckpilot <command> [args]

Commands:
  send    <session> <message...>         Send message to a session
  show    [session] [history]            Get session buffer
  list                                   List active sessions (alias: ls)
  launch  <agent> <prompt...> [--cwd D]  Start agent in new Ghostty window
  watch   <session>                      Auto-approve prompts

Run 'deckpilot help' for this message.
`)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\nAvailable commands: send, show, list, ls, launch, watch\nRun 'deckpilot help' for usage.\n", os.Args[1])
		os.Exit(1)
	}
}
