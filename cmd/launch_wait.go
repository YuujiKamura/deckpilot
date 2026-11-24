package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

var launchIdleWait = waitForIdle
var launchIdleLookup = lookupStatus
var launchIdleShow = daemon.DaemonShowTab

func finishLaunchWait(args LaunchArgs, session, tab string) error {
	if args.NoWait {
		return nil
	}
	fmt.Printf("waiting for idle (%s", session)
	if tab != "" {
		fmt.Printf(" %s", tab)
	}
	fmt.Println(")")
	// The daemon can briefly retain the pre-send idle status. Require it to
	// remain terminal across watcher polls before accepting it.
	terminalSamples := 0
	lookup := func() (string, bool, error) {
		status, found, err := launchIdleLookup(session)
		if status == "dead" {
			status = "idle"
		}
		if status == "idle" || status == "stalled" {
			terminalSamples++
			if terminalSamples < 4 {
				status = "active"
			}
		} else {
			terminalSamples = 0
		}
		return status, found, err
	}
	if tab != "" {
		var previous string
		stable := 0
		first := true
		lookup = func() (string, bool, error) {
			content, _, err := launchIdleShow(session, "buffer", "", tab)
			if err != nil {
				return "", false, err
			}
			if first || content != previous {
				previous = content
				stable = 0
				first = false
				return "active", true, nil
			}
			stable++
			status := daemon.StableScreenStatus(content, stable)
			if status == "" {
				status = "active"
			}
			return status, true, nil
		}
	}
	status, _, err := launchIdleWait(args.WaitTimeout, 500*time.Millisecond, lookup)
	if err != nil {
		return fmt.Errorf("wait for idle: %w", err)
	}
	content, _, showErr := launchIdleShow(session, "buffer", "", tab)
	if showErr != nil {
		fmt.Fprintf(os.Stderr, "could not read terminal tail: %v\n", showErr)
	} else {
		fmt.Printf("--- last %d lines ---\n", args.WaitTail)
		if content != "" {
			fmt.Println(lastLines(content, args.WaitTail))
		}
	}
	if strings.HasPrefix(status, "timeout:") {
		return fmt.Errorf("not idle within %s", args.WaitTimeout)
	}
	if status == "gone" {
		return fmt.Errorf("session disappeared while waiting for idle")
	}
	fmt.Printf("%s: %s\n", session, status)
	return nil
}
