package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// pollVerdict is what a long-running monitor should do after one poll of the
// session it is bound to.
type pollVerdict int

const (
	pollOK    pollVerdict = iota // poll succeeded; process the result
	pollRetry                    // transient failure; skip this tick and poll again
	pollStop                     // give up; exit non-zero
)

func (v pollVerdict) String() string {
	switch v {
	case pollOK:
		return "ok"
	case pollRetry:
		return "retry"
	case pollStop:
		return "stop"
	}
	return fmt.Sprintf("pollVerdict(%d)", int(v))
}

// defaultTransientBudget is how long a session-bound monitor keeps retrying
// while the daemon is unreachable before giving up.
const defaultTransientBudget = 60 * time.Second

// minTransientFailures keeps very long poll intervals from giving up on the
// first hiccup.
const minTransientFailures = 3

// transientLimitFor converts a time budget into a consecutive-failure limit
// for a monitor that polls every interval (ceil(budget/interval), at least
// minTransientFailures).
func transientLimitFor(interval, budget time.Duration) int {
	if interval <= 0 {
		return minTransientFailures
	}
	n := int((budget + interval - 1) / interval)
	if n < minTransientFailures {
		n = minTransientFailures
	}
	return n
}

// sessionPollGuard decides whether a monitor bound to one session keeps
// polling. It separates two failure classes, in the same spirit as
// isSubmitFailure in launch_retry.go ("retrying cannot help"):
//
//   - daemon.ErrSessionNotFound: permanent. The daemon re-scans sessions
//     before answering not-found, so the session is gone; stop at once.
//   - any other error (pipe dial failure, no response, decode error):
//     transient. Retry, but stop once more than maxConsecutive failures occur
//     in a row, so a dead daemon cannot keep the monitor spinning forever.
//
// It also suppresses repeats: an identical consecutive error is reported
// once, not once per tick (the 740KB approve.log incident, 2026-11-14).
type sessionPollGuard struct {
	maxConsecutive int // transient failures tolerated in a row; one more stops

	consecutive int
	lastMsg     string
}

func newSessionPollGuard(maxConsecutive int) *sessionPollGuard {
	if maxConsecutive < 1 {
		maxConsecutive = 1
	}
	return &sessionPollGuard{maxConsecutive: maxConsecutive}
}

// Observe records the outcome of one poll. It returns the verdict and a log
// line to print; the line is "" when there is nothing new to say (a success
// after a success, or the same transient error as the previous tick).
func (g *sessionPollGuard) Observe(err error) (pollVerdict, string) {
	if err == nil {
		line := ""
		if g.consecutive > 0 {
			line = fmt.Sprintf("recovered after %d failed poll(s)", g.consecutive)
		}
		g.consecutive = 0
		g.lastMsg = ""
		return pollOK, line
	}
	if errors.Is(err, daemon.ErrSessionNotFound) {
		return pollStop, fmt.Sprintf("%v (session is gone, stopping)", err)
	}
	g.consecutive++
	if g.consecutive > g.maxConsecutive {
		return pollStop, fmt.Sprintf("%v (giving up after %d consecutive failures)", err, g.consecutive)
	}
	msg := err.Error()
	if msg == g.lastMsg {
		return pollRetry, ""
	}
	g.lastMsg = msg
	return pollRetry, fmt.Sprintf("%v (retrying, up to %d consecutive failures)", err, g.maxConsecutive)
}

// superviseSessionPresence polls probe every interval until ctx ends or the
// guard says stop. probe returns nil while the session exists, an error
// wrapping daemon.ErrSessionNotFound once it is gone, and any other error for
// a transient daemon failure. Lines from the guard are written to w with the
// given prefix. It returns true when it stopped because of the guard (the
// caller should exit non-zero) and false when ctx was cancelled.
func superviseSessionPresence(ctx context.Context, probe func() error, interval time.Duration, guard *sessionPollGuard, w io.Writer, prefix string) bool {
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			verdict, line := guard.Observe(probe())
			if line != "" {
				fmt.Fprintf(w, "%s: %s\n", prefix, line)
			}
			if verdict == pollStop {
				return true
			}
		}
	}
}
