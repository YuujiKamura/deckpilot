package cmd

import (
	"fmt"
	"os"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// Notify manages idle notification hooks
func Notify(args []string) {
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon start error: %v\n", err)
		os.Exit(1)
	}

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: deckpilot notify <add|remove|list> [args]\n")
		os.Exit(1)
	}

	subcommand := args[0]
	switch subcommand {
	case "add":
		addNotifyHook(args[1:])
	case "remove":
		removeNotifyHooks()
	case "list":
		listNotifyHooks()
	default:
		fmt.Fprintf(os.Stderr, "Unknown notify subcommand: %s\nAvailable: add, remove, list\n", subcommand)
		os.Exit(1)
	}
}

func addNotifyHook(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, `Usage: deckpilot notify add <type> [args]

Types:
  stdout [message]                     Print to stdout when session becomes idle
  http <url> [method] [headers...]     Send HTTP request (default: POST)
  send <target_session> [message]     Send message to another session

Examples:
  deckpilot notify add stdout "Task completed"
  deckpilot notify add http https://hooks.slack.com/webhook POST
  deckpilot notify add send main-session "Background task finished"

Optional filter:
  Add --session <name> to only trigger for specific sessions

`)
		os.Exit(1)
	}

	hookType := args[0]
	args = args[1:]

	var hook daemon.IdleHook
	hook.Type = hookType

	// Parse session filter
	sessionFilter := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--session" && i+1 < len(args) {
			sessionFilter = args[i+1]
			// Remove --session and value from args
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	hook.SessionFilter = sessionFilter

	switch hookType {
	case "stdout":
		if len(args) > 0 {
			hook.Message = args[0]
		}

	case "http":
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "http hook requires URL\n")
			os.Exit(1)
		}
		hook.URL = args[0]
		if len(args) > 1 {
			hook.Method = args[1]
		}
		hook.Headers = make(map[string]string)
		// Parse additional headers (key=value format)
		for i := 2; i < len(args); i++ {
			// Simple key=value parsing
			if j := findChar(args[i], '='); j != -1 {
				key := args[i][:j]
				value := args[i][j+1:]
				hook.Headers[key] = value
			}
		}

	case "send":
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "send hook requires target session\n")
			os.Exit(1)
		}
		hook.TargetSession = args[0]
		if len(args) > 1 {
			hook.Message = args[1]
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown hook type: %s\nAvailable: stdout, http, send\n", hookType)
		os.Exit(1)
	}

	// Send hook to daemon via IPC
	if err := daemon.DaemonAddIdleHook(hook); err != nil {
		fmt.Fprintf(os.Stderr, "Add hook error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Added %s idle hook", hookType)
	if sessionFilter != "" {
		fmt.Printf(" for session '%s'", sessionFilter)
	}
	fmt.Println()
}

func removeNotifyHooks() {
	if err := daemon.DaemonRemoveIdleHooks(); err != nil {
		fmt.Fprintf(os.Stderr, "Remove hooks error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Removed all idle hooks")
}

func listNotifyHooks() {
	hooks, err := daemon.DaemonListIdleHooks()
	if err != nil {
		fmt.Fprintf(os.Stderr, "List hooks error: %v\n", err)
		os.Exit(1)
	}

	if len(hooks) == 0 {
		fmt.Println("No idle hooks configured")
		return
	}

	fmt.Printf("Configured idle hooks (%d):\n", len(hooks))
	for i, hook := range hooks {
		fmt.Printf("%d. Type: %s", i+1, hook.Type)
		if hook.SessionFilter != "" {
			fmt.Printf(" (session: %s)", hook.SessionFilter)
		}
		fmt.Println()

		switch hook.Type {
		case "stdout":
			msg := hook.Message
			if msg == "" {
				msg = "(default message)"
			}
			fmt.Printf("   Message: %s\n", msg)

		case "http":
			method := hook.Method
			if method == "" {
				method = "POST"
			}
			fmt.Printf("   URL: %s (%s)\n", hook.URL, method)
			if len(hook.Headers) > 0 {
				fmt.Printf("   Headers: %v\n", hook.Headers)
			}

		case "send":
			msg := hook.Message
			if msg == "" {
				msg = "(default message)"
			}
			fmt.Printf("   Target: %s\n   Message: %s\n", hook.TargetSession, msg)
		}
		fmt.Println()
	}
}

// Helper function to find character in string
func findChar(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
