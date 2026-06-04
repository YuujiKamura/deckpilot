package daemon

import (
	"log"
	"sync/atomic"
	"time"
)

// Production timing for the self-watchdog. These are passed into selfWatchdog
// at its single call site in Run(); tests pass their own shortened values, so
// nothing here is a mutable package global (which would data-race under -race).
const (
	watchdogInterval = 30 * time.Second // how often the watchdog evaluates liveness
	watchdogTimeout  = 60 * time.Second // stale-tick threshold (6 missed 10s discovery ticks)
)

// selfWatchdog watches discoveryLoop's last tick time and exits the daemon
// process if no tick has been observed for `timeout`. Exiting releases the
// singleton mutex and named pipe that a hung daemon would otherwise hold, so
// the next `deckpilot` invocation (via EnsureRunning) can spawn a fresh
// daemon. There is no daemon-less auto-respawn: the watchdog converts a "hung"
// daemon (actively blocking respawn) into a "dead" one (harmless, revived on
// next use).
//
// Defense-in-depth pair with the external PING path: the external path covers
// cases where the runtime itself is wedged (this goroutine cannot fire); this
// path covers cases where the discovery loop wedges but the runtime is
// otherwise healthy.
//
// interval/timeout/exit/stop are injected (not package globals) so tests run
// independent, race-free instances. The grace period is implicit: Run() seeds
// lastDiscoveryTick=now before starting discovery, so the daemon is not judged
// until `timeout` has elapsed from startup. A lastDiscoveryTick of 0 (never
// seeded or ticked) reads as the Unix epoch — far older than any timeout — so
// it exits, treating "discovery never ran" as the deeper bug it is.
func (d *Daemon) selfWatchdog(interval, timeout time.Duration, exit func(int), stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			last := atomic.LoadInt64(&d.lastDiscoveryTick)
			age := time.Since(time.Unix(0, last))
			if age > timeout {
				log.Printf("daemon: self-watchdog: last discovery tick %v ago (>%v), exiting", age, timeout)
				exit(1)
				// In production os.Exit never returns; in tests the injected
				// fake exit does return, so stop the goroutine here to avoid
				// looping (and leaking) after a detected hang.
				return
			}
		}
	}
}
