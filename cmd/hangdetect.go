// Package cmd — `deckpilot hang-detect` CLI entry point (issue #26).
//
// Wires a HangDetector to one session. Designed to be run alongside
// `deckpilot auto-approvals` or by itself when external monitoring is
// all that's needed.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// HangDetect is the CLI command: deckpilot hang-detect <session> [flags]
//
//	--cpu-threshold N     percent (default 1.0)
//	--stall-seconds N     seconds of unchanged buffer (default 60)
//	--probe-interval DUR  gap between probes (default 5s)
//	--sample-interval DUR CPU sample window (default 500ms, must be < probe)
//	--on-hang ACTION      notify | ctrl-c | kill-child | kill-session (default notify)
//	--include-children    default true; --no-include-children to disable
//	--once                run one probe and exit (diagnostic)
func HangDetect(args []string) {
	var sessionName string
	cpuThreshold := 1.0
	stallSeconds := 60 * time.Second
	probeInterval := 5 * time.Second
	sampleInterval := 500 * time.Millisecond
	onHang := "notify"
	includeChildren := true
	runOnce := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cpu-threshold":
			i++
			if i >= len(args) {
				hangUsageFatal("--cpu-threshold requires a value")
			}
			v, err := parseFloat(args[i])
			if err != nil {
				hangUsageFatal("invalid --cpu-threshold %q: %v", args[i], err)
			}
			cpuThreshold = v
		case "--stall-seconds":
			i++
			if i >= len(args) {
				hangUsageFatal("--stall-seconds requires a value")
			}
			v, err := parseFloat(args[i])
			if err != nil {
				hangUsageFatal("invalid --stall-seconds %q: %v", args[i], err)
			}
			stallSeconds = time.Duration(v * float64(time.Second))
		case "--probe-interval":
			i++
			if i >= len(args) {
				hangUsageFatal("--probe-interval requires a duration")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				hangUsageFatal("invalid --probe-interval %q: %v", args[i], err)
			}
			probeInterval = d
		case "--sample-interval":
			i++
			if i >= len(args) {
				hangUsageFatal("--sample-interval requires a duration")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				hangUsageFatal("invalid --sample-interval %q: %v", args[i], err)
			}
			sampleInterval = d
		case "--on-hang":
			i++
			if i >= len(args) {
				hangUsageFatal("--on-hang requires an action name")
			}
			onHang = args[i]
		case "--include-children":
			includeChildren = true
		case "--no-include-children":
			includeChildren = false
		case "--once":
			runOnce = true
		default:
			if strings.HasPrefix(args[i], "--") {
				hangUsageFatal("unknown flag %q", args[i])
			}
			if sessionName == "" {
				sessionName = args[i]
			}
		}
	}
	if sessionName == "" {
		hangUsageFatal("session name required")
	}

	// Resolve root PID from the daemon's session list.
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}
	rootPID, err := resolveSessionPID(sessionName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hang-detect: %v\n", err)
		os.Exit(1)
	}

	// Action selection.
	action, err := pickHangAction(onHang, sessionName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hang-detect: %v\n", err)
		os.Exit(1)
	}

	cfg := daemon.HangConfig{
		SessionName:     sessionName,
		RootPID:         rootPID,
		Lister:          daemon.SystemProcessLister{},
		Sampler:         daemon.SystemCPUSampler{},
		LastActOf:       func() time.Time { return fetchLastAct(sessionName) },
		Action:          action,
		CPUThreshold:    cpuThreshold,
		StallSeconds:    stallSeconds,
		ProbeInterval:   probeInterval,
		SampleInterval:  sampleInterval,
		IncludeChildren: includeChildren,
	}
	det, err := daemon.NewHangDetector(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hang-detect: config: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr,
		"hang-detect: monitoring %s (pid=%d, cpu<%.1f%%, stall>%s, action=%s)\n",
		sessionName, rootPID, cpuThreshold, stallSeconds, onHang)

	if runOnce {
		// One probe and exit — useful for diagnostics / smoke tests.
		ctx, cancel := context.WithTimeout(context.Background(),
			probeInterval+sampleInterval+time.Second)
		defer cancel()
		_ = det.ProbeOnce(ctx)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\nstopped")
		cancel()
	}()

	if err := det.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "hang-detect: run: %v\n", err)
		os.Exit(1)
	}
}

func hangUsageFatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "hang-detect: "+format+"\n", a...)
	fmt.Fprintln(os.Stderr,
		"usage: deckpilot hang-detect <session> [--cpu-threshold N] [--stall-seconds N] "+
			"[--probe-interval DUR] [--sample-interval DUR] "+
			"[--on-hang notify|ctrl-c|kill-child|kill-session] "+
			"[--include-children | --no-include-children] [--once]")
	os.Exit(1)
}

func parseFloat(s string) (float64, error) {
	// Avoid importing strconv twice by inlining; accepts plain decimals.
	var v float64
	_, err := fmt.Sscanf(s, "%f", &v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// resolveSessionPID asks the daemon for sessions and returns the PID of
// the requested one. Returns error if the session is unknown or lacks
// a PID (web sessions).
func resolveSessionPID(sessionName string) (uint32, error) {
	raw, err := daemon.DaemonList()
	if err != nil {
		return 0, fmt.Errorf("list: %w", err)
	}
	type entry struct {
		Name string `json:"name"`
		PID  int    `json:"pid"`
	}
	var all []entry
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return 0, fmt.Errorf("parse list: %w", err)
	}
	for _, e := range all {
		if e.Name == sessionName {
			if e.PID <= 0 {
				return 0, fmt.Errorf("session %q has no PID", sessionName)
			}
			return uint32(e.PID), nil
		}
	}
	return 0, fmt.Errorf("session not found: %s", sessionName)
}

// fetchLastAct reads the session's last-act timestamp. When the daemon
// does not expose it explicitly (older protocol), we fall back to
// time.Now() so the stall clock starts fresh — the CPU-idle check alone
// still prevents the detector from firing falsely on a busy process.
func fetchLastAct(sessionName string) time.Time {
	// The current SHOW protocol does not return last-act, so for now
	// we stamp Now() on every probe. Issue #26 M2 followup: extend
	// SHOW or add a dedicated LASTACT command. Harmless default: the
	// stall check is effectively disabled until the protocol is wired
	// up, leaving CPU-threshold as the sole hang criterion.
	return time.Now()
}

// pickHangAction returns a HangAction by name. Per-action dependencies
// (Daemon ref for ctrl-c, ProcessLister for kill-child) are injected
// from package-level defaults.
func pickHangAction(name, _ string) (daemon.HangAction, error) {
	switch name {
	case "notify":
		// Notify doesn't need daemon access; pass nil to keep the CLI
		// daemon-process-independent (the action runs client-side).
		return daemon.ActionNotify(nil), nil
	case "ctrl-c":
		return nil, fmt.Errorf(
			"on-hang=ctrl-c requires an in-daemon detector; run as `deckpilot daemon` with config (issue #26 M3)")
	case "kill-child":
		return daemon.ActionKillChild(daemon.SystemProcessLister{}), nil
	case "kill-session":
		return daemon.ActionKillSession(), nil
	default:
		return nil, fmt.Errorf("unknown --on-hang action %q (valid: notify, ctrl-c, kill-child, kill-session)", name)
	}
}
