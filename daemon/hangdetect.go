// Package daemon — HangDetector (issue #26).
//
// Observes a session's watcher and the OS process(es) behind it; when
// both (a) CPU utilization stays below a threshold AND (b) the session
// buffer has not changed for longer than a stall threshold, the agent
// is considered hung and a user-configured action is dispatched
// (notify, Ctrl+C, kill-child, or kill-session).
//
// This file is platform-agnostic. Windows-specific syscalls live in
// proctree_windows.go and cpu_sample_windows.go behind the
// ProcessLister / CPUSampler interfaces, so the detector itself can be
// unit-tested with stubs on any platform.
package daemon

import (
	"context"
	"fmt"
	"log"
	"time"
)

// HangAction is dispatched when a hang is detected. Implementations
// perform the actual remediation (log, PTY Ctrl+C, Stop-Process, ...).
//
// Returning an error is informational — the detector continues
// regardless; a subsequent probe will re-trigger the action if the
// hang persists beyond the cooldown window.
type HangAction func(ctx context.Context, info HangInfo) error

// HangInfo is the evidence the detector collected before firing an
// action. Passed to HangAction implementations so they can log or
// escalate based on specifics.
type HangInfo struct {
	SessionName  string
	RootPID      uint32
	ProcessCount int           // including root
	CPUPercent   float64       // aggregate over the last sample interval
	StallSince   time.Duration // how long since last-act updated
	DetectedAt   time.Time

	// Source identifies which signal fired the action. Drives log
	// severity and operator trust:
	//   "os-hung"    — Win32 IsHungAppWindow says the message pump
	//                  has stopped. Authoritative; same criterion
	//                  Task Manager uses.
	//   "heuristic"  — CPU below threshold AND stall exceeded.
	//                  Best-effort; may false-positive on low-CPU
	//                  workloads that are still making progress.
	Source string
}

// HangConfig tunes the detector. All fields are required except
// IncludeChildren (default true) and the clock/sampler overrides
// (production code uses the real ones).
type HangConfig struct {
	SessionName  string
	RootPID      uint32
	Lister       ProcessLister
	Sampler      CPUSampler
	LastActOf    func() time.Time // returns last-act timestamp for the session
	Action       HangAction

	// WatcherStatusOf is the OS-authoritative fast path (issue #26
	// pivot). If non-nil, probeOnce calls it and fires Action
	// immediately when the returned status is "stalled" — the
	// Watcher's code word for IsHungAppWindow(hwnd)==TRUE. CPU + stall
	// are skipped in that branch so Task Manager's "Not Responding"
	// verdict translates to an action in one probe.
	//
	// Leave nil to disable the fast path (tests that want to
	// exercise the heuristic branch explicitly, web sessions without
	// a window handle, etc.).
	WatcherStatusOf func() string

	CPUThreshold   float64       // e.g. 1.0 (percent, aggregate)
	StallSeconds   time.Duration // e.g. 60 * time.Second
	ProbeInterval  time.Duration // e.g. 5 * time.Second
	SampleInterval time.Duration // e.g. 500 * time.Millisecond
	Cooldown       time.Duration // minimum gap between two Action dispatches, e.g. 2× StallSeconds

	IncludeChildren bool // default true; disable to watch only root PID

	// Optional overrides for tests. Nil → time.Now / time.After.
	Now   func() time.Time
	Sleep func(time.Duration)
}

// Validate returns an error if any required field is missing or a
// threshold is nonsensical. Call before Run.
func (c *HangConfig) Validate() error {
	if c.SessionName == "" {
		return fmt.Errorf("SessionName required")
	}
	if c.RootPID == 0 {
		return fmt.Errorf("RootPID required")
	}
	if c.Lister == nil {
		return fmt.Errorf("Lister required")
	}
	if c.Sampler == nil {
		return fmt.Errorf("Sampler required")
	}
	if c.LastActOf == nil {
		return fmt.Errorf("LastActOf required")
	}
	if c.Action == nil {
		return fmt.Errorf("Action required")
	}
	if c.CPUThreshold < 0 {
		return fmt.Errorf("CPUThreshold must be ≥ 0")
	}
	if c.StallSeconds <= 0 {
		return fmt.Errorf("StallSeconds must be > 0")
	}
	if c.ProbeInterval <= 0 {
		return fmt.Errorf("ProbeInterval must be > 0")
	}
	if c.SampleInterval <= 0 {
		return fmt.Errorf("SampleInterval must be > 0")
	}
	if c.SampleInterval >= c.ProbeInterval {
		return fmt.Errorf("SampleInterval must be < ProbeInterval")
	}
	return nil
}

// HangDetector runs a probe loop until ctx is cancelled. One detector
// per watched session. Start with Run; safe to stop by cancelling ctx.
type HangDetector struct {
	cfg         HangConfig
	lastDispatch time.Time
}

// NewHangDetector constructs a detector; validates the config up front.
func NewHangDetector(cfg HangConfig) (*HangDetector, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	// Default cooldown: 2× stall to avoid repeated fires while the
	// operator is still investigating.
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 2 * cfg.StallSeconds
	}
	return &HangDetector{cfg: cfg}, nil
}

// Run probes until ctx is cancelled. Returns ctx.Err() on cancellation
// or a fatal error from a probe (Lister failure). Transient errors
// (Sampler returning nonzero err) are logged and skipped.
func (d *HangDetector) Run(ctx context.Context) error {
	log.Printf("hangdetect[%s]: starting (root=%d, cpu<%.1f%%, stall>%s)",
		d.cfg.SessionName, d.cfg.RootPID, d.cfg.CPUThreshold, d.cfg.StallSeconds)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := d.ProbeOnce(ctx); err != nil {
			// Fatal — e.g. lister snapshot failure we can't recover from.
			return err
		}

		// Wait for next probe. Use a select so cancel is responsive.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.cfg.ProbeInterval):
		}
	}
}

// ProbeOnce performs a single CPU sample + last-act check + action
// dispatch. Exported so the CLI --once path and external tests can call
// it directly; the Run loop invokes the same logic internally.
func (d *HangDetector) ProbeOnce(ctx context.Context) error {
	return d.probeOnce(ctx)
}

// inCooldown reports whether the most recent Action dispatch happened
// within the configured cooldown window. Kept as a separate method so
// both the os-hung fast path and the heuristic slow path share the
// same suppression logic.
func (d *HangDetector) inCooldown() bool {
	if d.lastDispatch.IsZero() {
		return false
	}
	return d.cfg.Now().Sub(d.lastDispatch) < d.cfg.Cooldown
}

// probeOnce is the internal implementation, kept unexported so tests in
// the same package can exercise it without the ctx dance when they
// want to assert side-effects directly.
func (d *HangDetector) probeOnce(ctx context.Context) error {
	// 0. OS-authoritative fast path (issue #26 pivot).
	//    The Watcher polls IsHungAppWindow(hwnd) every tick and flips
	//    its status to "stalled" when Windows itself declares the
	//    window non-responsive. That is the same signal Task Manager
	//    uses; trust it over CPU/stall heuristics.
	if d.cfg.WatcherStatusOf != nil {
		if d.cfg.WatcherStatusOf() == "stalled" {
			if d.inCooldown() {
				return nil
			}
			info := HangInfo{
				SessionName: d.cfg.SessionName,
				RootPID:     d.cfg.RootPID,
				DetectedAt:  d.cfg.Now(),
				Source:      "os-hung",
			}
			log.Printf("hangdetect[%s]: HANG DETECTED via os-hung (IsHungAppWindow)",
				info.SessionName)
			d.lastDispatch = d.cfg.Now()
			if err := d.cfg.Action(ctx, info); err != nil {
				log.Printf("hangdetect[%s]: action error: %v", info.SessionName, err)
			}
			return nil
		}
	}

	// 1. Enumerate target pids.
	var pids []uint32
	if d.cfg.IncludeChildren {
		tree, err := GetProcessTree(d.cfg.Lister, d.cfg.RootPID)
		if err != nil {
			// Root process might have died between session listing
			// and our probe. That's not itself a hang — log and skip.
			log.Printf("hangdetect[%s]: process tree: %v (skipping)",
				d.cfg.SessionName, err)
			return nil
		}
		pids = make([]uint32, 0, len(tree))
		for _, p := range tree {
			pids = append(pids, p.PID)
		}
	} else {
		pids = []uint32{d.cfg.RootPID}
	}

	// 2. CPU sample.
	percent, err := d.cfg.Sampler.Sample(pids, d.cfg.SampleInterval)
	if err != nil {
		log.Printf("hangdetect[%s]: sampler: %v (skipping)",
			d.cfg.SessionName, err)
		return nil
	}

	// 3. Last-act stall.
	lastAct := d.cfg.LastActOf()
	var stall time.Duration
	if !lastAct.IsZero() {
		stall = d.cfg.Now().Sub(lastAct)
	}

	// 4. Decision.
	idle := percent < d.cfg.CPUThreshold
	stalled := stall >= d.cfg.StallSeconds
	if !(idle && stalled) {
		return nil
	}

	// 5. Cooldown.
	if d.inCooldown() {
		return nil
	}

	info := HangInfo{
		SessionName:  d.cfg.SessionName,
		RootPID:      d.cfg.RootPID,
		ProcessCount: len(pids),
		CPUPercent:   percent,
		StallSince:   stall,
		DetectedAt:   d.cfg.Now(),
		Source:       "heuristic",
	}
	log.Printf("hangdetect[%s]: HANG DETECTED via heuristic (cpu=%.2f%%, stall=%s, procs=%d)",
		info.SessionName, info.CPUPercent, info.StallSince, info.ProcessCount)

	d.lastDispatch = d.cfg.Now()
	if err := d.cfg.Action(ctx, info); err != nil {
		log.Printf("hangdetect[%s]: action error: %v", info.SessionName, err)
	}
	return nil
}
