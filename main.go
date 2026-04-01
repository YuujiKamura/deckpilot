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
	case "status":
		cmd.Status(os.Args[2:])
	case "output":
		cmd.Output(os.Args[2:])
	case "history":
		cmd.History(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
