package cmd

import (
	"os"
	"strconv"
)

// getCaller returns a caller identifier for lastUsedSession tracking.
// Priority:
//  1. DECKPILOT_CALLER env var (explicit override)
//  2. WT_SESSION (Windows Terminal tab/pane GUID)
//  3. GHOSTTY_SESSION (if Ghostty sets one in the future)
//  4. PPID (Linux/macOS where it's stable)
//  5. "default" fallback
func getCaller() string {
	if v := os.Getenv("DECKPILOT_CALLER"); v != "" {
		return v
	}
	if v := os.Getenv("WT_SESSION"); v != "" {
		return "wt:" + v
	}
	if v := os.Getenv("GHOSTTY_SESSION"); v != "" {
		return "ghostty:" + v
	}
	ppid := os.Getppid()
	if ppid > 1 {
		return "pid:" + strconv.Itoa(ppid)
	}
	return "default"
}
