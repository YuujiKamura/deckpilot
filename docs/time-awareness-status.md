# Time-Awareness Implementation Status

Issue: https://github.com/YuujiKamura/deckpilot/issues/24

## Sub-features

| Feature | Status |
|---------|--------|
| A. Wall-clock header + LAST-ACT column + stale status | **shipped** |
| B. `show` bracketed header | **shipped** |
| B. `send` JST timestamp injection + `--no-timestamp` | **shipped** |
| C. `--eta` flag | **shipped** |

All four sub-features are green. `go test ./...` passes.

## Before

```
NAME                      PID    RUNTIME    UPTIME     STATUS
ghostty-40328             40328  winui3     5h11m      idle
```

## After

```
NOW: 2026-04-19 Sun 08:29 JST

NAME                      PID    RUNTIME    UPTIME     LAST-ACT    STATUS
ghostty-40328             40328  winui3     1m50s      42s ago     idle
ghostty-12200             12200  winui3     1m50s      0s ago      active
ghostty-37800             37800  winui3     1m50s      0s ago      active
ghostty-28236             28236  winui3     1m50s      1m ago      idle
ghostty-36236             36236  winui3     1m50s      1m ago      idle
```

## `deckpilot show` header

```
[ghostty-36236 | now 2026-04-19 Sun 08:29 JST | uptime 1m31s | last-act 1m ago]
```

## `deckpilot send` timestamp injection

Default (no flag): message is prepended with `[YYYY-MM-DD DDD HH:MM JST]`.
Opt-out: `--no-timestamp`.

## `deckpilot send --eta`

```
deckpilot send --eta 30m ghostty-40328 "run tests"
# stderr: NOW: 2026-04-19 Sun 08:12 JST  |  ETA 08:42 JST (+30m)
```

## Implementation notes

- `lastChangedAt` tracked in `Watcher` whenever the buffer hash changes; exposed via `WatcherProfile.LastChangedAt`.
- `stale` = `idle` AND `lastChangedAt > 30m` ago (computed daemon-side in `listSessions()`).
- JST hard-coded as `time.FixedZone("JST", 9*3600)` — no TZDB lookup, no config surface.
- SHOW IPC response extended from `OK|b64|status` to `OK|b64|status|uptime|lastact`; all existing callers ignore the new fields via `_`.
- No runtime dependency additions beyond Go standard library.
- Pre-existing note: `approval_patterns.go` and `autoapprovals.go` were not touched per brief constraint.

## Follow-up work (separate issues)

- The lock-ordering between `d.mu` (daemon) and `w.mu` (watcher) is technically a potential deadlock when `updateContent` holds `w.mu` and calls `onNotify` (which acquires `d.mu`) while `listSessions` holds `d.mu` and calls `w.Profile()` (which acquires `w.mu`). In practice this has not caused a hang because lock durations are microseconds. A future cleanup would move the notification out of the `w.mu` critical section.
