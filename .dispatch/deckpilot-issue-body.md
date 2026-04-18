## Problem

Last night: Claude A produced a plan at 03:33 JST; the dispatcher stopped; Codex B stayed idle; nothing advanced until the human returned at 08:10 JST — a **4.5-hour blind spot**. `deckpilot list` showed `idle` but gave no wall-clock or last-activity cue, so neither human nor agent noticed the stall.

## Sub-features

### A. Wall-clock + last-activity columns (required)

Current `deckpilot list` output:
```
NAME                      PID    RUNTIME    UPTIME     STATUS
ghostty-40328             40328  winui3     5h11m      idle
```

New shape — header line above the table plus `LAST-ACT` column:
```
NOW: 2026-04-19 Sun 08:10 JST

NAME                      PID    RUNTIME    UPTIME     LAST-ACT    STATUS
ghostty-40328             40328  winui3     5h13m      2m ago      idle
ghostty-36236             36236  winui3     5h16m      4h37m       stale
```

- `LAST-ACT` = duration since the session's output buffer last changed (tracked via in-process hash comparison; no extra syscall).
- `STATUS` gains a new value `stale` = idle AND last-act > 30 min. Existing values unchanged.
- Header line `NOW: ...` is emitted from `time.Now().In(JST)`. JST = `time.FixedZone("JST", 9*3600)`.

`deckpilot show <id>` emits the same header as its first line:
```
[ghostty-40328 | now 2026-04-19 Sun 08:10 JST | uptime 5h13m | last-act 2m ago]
```

### B. Send timestamp injection (required)

`deckpilot send <id> <msg>` prepends a JST timestamp to `<msg>`:
```
deckpilot send ghostty-40328 "run the tests"
# wire actually sent: "[2026-04-19 Sun 08:12 JST] run the tests"
```

Opt-out: `--no-timestamp` flag on `send`. Default **on**.

Rationale: sub-agents (claude, codex, gemini) have zero time-grounding inside their session. Prefixing each inbound message gives them a coarse clock.

### C. ETA helper (optional)

`--eta <duration>` flag on `send` prints a one-line note to stderr after submission:
```
deckpilot send --eta 30m ghostty-40328 "atom 03"
# stderr: NOW: 2026-04-19 Sun 08:12 JST  |  ETA 08:42 JST (+30m)
```

No agent-side effect; pure human-side display.

## Acceptance criteria

- `go test ./...` passes with all new tests green.
- Existing `list` / `show` / `send` behaviour is unchanged when new flags are absent and no `--no-timestamp` override is needed (timestamp is additive — it's the wire message that changes, not the CLI surface).
- No new external dependencies beyond the Go standard library.

## Non-goals

- No GUI changes.
- No cron / scheduled triggers.
- No TZ configuration surface — JST is hard-coded for this repo's operator.
- Do not touch `approval_patterns.go` or `autoapprovals.go`.
