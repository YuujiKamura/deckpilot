package cmd

import (
	"errors"
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

// settleSamplesForReady is how many consecutive unchanged screens count as
// "the program is waiting for input" when no ready marker is known.
const settleSamplesForReady = 3

// looksLikeMenu reports whether the screen is asking for a choice: numbered
// options 1, 2, ... on consecutive lines, one of them under a selection
// cursor. A menu is never "ready", even though its cursor can be the same
// character as an agent's prompt marker (Codex: "› 1. Update now").
func looksLikeMenu(screen string) bool {
	if looksLikeChoiceFooter(screen) {
		return true
	}
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

// errSessionLeftOpen marks a launch failure that needs a person to look at
// the window, so the launch must not close it.
var errSessionLeftOpen = errors.New("session left open for inspection")

// looksLikeChoiceFooter reports a key hint that only a selection prompt
// shows ("up/down Navigate", "enter Confirm", "(y/n)"), read from the last
// few lines so that ordinary output mentioning these words does not count.
func looksLikeChoiceFooter(screen string) bool {
	seen := 0
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0 && seen < 6; i-- {
		line := strings.ToLower(strings.TrimSpace(lines[i]))
		if line == "" {
			continue
		}
		seen++
		if strings.Contains(line, "↑/↓") || strings.Contains(line, "(y/n)") || strings.Contains(line, "[y/n]") {
			return true
		}
		if strings.Contains(line, "enter") && strings.Contains(line, "confirm") {
			return true
		}
	}
	return false
}

// launchCommandMarker is the start of the command line Launch types into
// the shell. While it is still the last thing on screen, the program has
// not drawn anything yet.
const launchCommandMarker = "cmd /k "

func drewAfterLaunch(screen string) bool {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return !strings.Contains(line, launchCommandMarker)
	}
	return false
}
