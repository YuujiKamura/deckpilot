# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] - 2026-06-04

### Added

- `deckpilot conduct --file PATH [--agent NAME] [--interval 30s] [--one-shot]` — watch a markdown checklist and dispatch a `deckpilot launch` per unchecked `- [ ]` line. The line is rewritten to `- [x]` before the launch so the same task is never dispatched twice. Minimal substrate for the "tasks → autonomous coding agent" loop: drop items into one `.md` and the orchestrator runs them without per-task hand-off. (7415741)
- `deckpilot cleanup --worktrees` — opt-in flag (default off) that sweeps orphan worktrees under `~/.deckpilot/worktrees/` that no live session depends on. The existing launch-meta liveness sweep only removes a worktree while its session's meta still exists, so dirs whose meta was already GC'd lingered with no path to reclaim them. Daemon-gated: if the live-session list cannot be fetched the sweep aborts and removes nothing, and a directory a process is still cwd'd into is protected by the OS file lock. Real-world dogfood reclaimed 43 worktrees down to 6, leaving only the one in-use dir it correctly declined to remove. (ea47352)
- Daemon-internal self-watchdog goroutine for hang recovery. A goroutine watches the discovery loop's last tick time and calls `os.Exit(1)` if no tick is observed past the 60 s timeout (30 s probe interval). A hung daemon keeps holding the singleton mutex and named pipe, which blocks any respawn; exiting releases both so the next `deckpilot` invocation spawns a fresh one — converting a "hung" daemon (blocking restart) into a "dead" one (harmless, revived on next use). Shared state is `lastDiscoveryTick` (atomic int64) only; it deliberately does not auto-respawn. (b28f583)
- Daemon singleton enforcement via a `Global\` named kernel mutex (`Global\deckpilot-daemon-singleton`), acquired at the top of `daemon.Run()`. "First instance wins": a second daemon launch exits with `another instance is already running (singleton mutex Global\deckpilot-daemon-singleton is held)` and the running daemon stays untouched. This replaces the previous takeover-only design, under which builds with different pipe paths or unresponsive daemons could slip past `SHUTDOWN` and produce ghost siblings (observed: 6 `deckpilot.exe` alive concurrently with `list` returning "no response from daemon"). `DECKPILOT_SINGLETON_MUTEX_SUFFIX` lets parallel tests use unique mutex names. (97de0c6)

### Fixed

- `deckpilot launch` no longer leaks Ghostty windows on `submit_failed_stuck`. Previously every post-`Start()` exit path called `os.Exit(1)` without killing the already-started Ghostty window, so callers retrying a failed launch spawned more windows (2026-06-04: 2 intended dispatches leaked 5 extra windows). Now all four post-`Start()` exit paths in `cmd/launch.go` (waitForNewSession, launchCmd send, waitForReady, prompt send) kill the leaked Ghostty PID, making caller retry idempotent. The two send sites additionally retry first: a genuine stuck leaves the typed text in the input box, so the retry nudges Enter only (empty message) rather than re-typing and doubling it, and confirms recovery via buffer-hash movement before declaring a real failure. (c556e46, d7d212b)
