package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/YuujiKamura/deckpilot/daemon"
)

func Tab(args []string) {
	if err := tabCommand(args); err != nil {
		fmt.Fprintln(os.Stderr, "tab:", err)
		os.Exit(1)
	}
}
func tabCommand(args []string) error {
	if len(args) < 2 || (args[0] == "new" && len(args) != 2) || (args[0] == "close" && len(args) != 3) || (args[0] != "new" && args[0] != "close") {
		return fmt.Errorf("usage: deckpilot tab new <session> | tab close <session> <tab>")
	}
	if err := daemon.EnsureRunning(); err != nil {
		return err
	}
	tab := ""
	if len(args) == 3 {
		tab = args[2]
	}
	out, err := daemon.DaemonTab(args[1], args[0], tab)
	if err != nil {
		return err
	}
	if args[0] == "close" {
		fmt.Print(formatTabList(out))
	} else {
		fields := strings.SplitN(out, "|", 2)
		if len(fields) != 2 {
			return fmt.Errorf("invalid new tab response")
		}
		fmt.Printf("tab: %s id=%s\n", fields[0], fields[1])
	}
	return nil
}
