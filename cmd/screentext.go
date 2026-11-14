package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// maxBlindSamples is how many consecutive screen reads may fail before the
// ready wait gives up and reports that it cannot see the session.
const maxBlindSamples = 5

// launchScreenLines is how much of the screen a failed launch hands back.
const launchScreenLines = 20

var readyPollInterval = 1 * time.Second

// printLaunchScreen writes the last screen deckpilot saw, or says plainly
// that it never saw one. deckpilot does not interpret the screen: whoever
// launched the session reads it and decides what to do.
func printLaunchScreen(w io.Writer, session string, d readyWaitDiagnostics) {
	screen := trimScreen(d.LastBuffer)
	if screen == "" {
		fmt.Fprintf(w, "screen of %s: never read (last error: %s)\n", session, d.LastShowError)
		return
	}
	fmt.Fprintf(w, "--- screen of %s (last %d lines) ---\n%s\n--- end screen ---\n",
		session, launchScreenLines, tailLines(screen, launchScreenLines))
}

// trimScreen drops the blank rows a terminal buffer carries below its text.
func trimScreen(buf string) string {
	return strings.TrimRight(buf, " \t\r\n")
}

// tailLines returns the last n lines of s with trailing spaces removed.
// n <= 0 returns every line.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	return strings.Join(lines, "\n")
}

// daemonShowFn reads a session screen; tests replace it.
var daemonShowFn = daemon.DaemonShow

// menuSamplesBeforeReport is how many consecutive reads must show the same
// kind of menu before the launch stops and hands the screen to the caller.
const menuSamplesBeforeReport = 3

// looksLikeMenu reports whether the screen is asking for a choice: numbered
// options 1, 2, ... on consecutive lines, one of them under a selection
// cursor. A menu is never "ready", even though its cursor can be the same
// character as an agent's prompt marker (Codex: "› 1. Update now").
func looksLikeMenu(screen string) bool {
	want := 1
	cursor := false
	for _, raw := range strings.Split(screen, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		marked := false
		for _, m := range []string{"›", "❯", ">", "*"} {
			if strings.HasPrefix(line, m) {
				marked = true
				line = strings.TrimSpace(strings.TrimPrefix(line, m))
				break
			}
		}
		if n, ok := choiceNumber(line); ok && n == want {
			want++
			cursor = cursor || marked
			continue
		}
		if want > 2 { // the run of choices ended
			break
		}
		want, cursor = 1, false
		if n, ok := choiceNumber(line); ok && n == 1 { // a new run starts here
			want, cursor = 2, marked
		}
	}
	return want > 2 && cursor
}

// choiceNumber parses a leading "N." or "N)" followed by a space and text.
func choiceNumber(line string) (int, bool) {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 2 || i+2 > len(line) || (line[i] != '.' && line[i] != ')') || line[i+1] != ' ' {
		return 0, false
	}
	if strings.TrimSpace(line[i+1:]) == "" {
		return 0, false
	}
	n := 0
	for _, c := range line[:i] {
		n = n*10 + int(c-'0')
	}
	return n, true
}
