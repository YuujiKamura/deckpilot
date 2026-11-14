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
