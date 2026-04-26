# deckpilot

A CLI tool for programmatic control of AI agents (Claude Code, Codex, Gemini) running in Ghostty terminal. Provides session discovery, message sending, buffer retrieval, auto-approval, hang detection with auto-snapshot, and idle notification hooks via Named Pipe.

## Prerequisites

- **Ghostty for Windows** ([ghostty-win](https://github.com/YuujiKamura/ghostty-win)) or **Windows Terminal** (requires `wt-sidecar`).
- Go 1.21+
- Windows 10/11

## Installation

```bash
go build -o deckpilot.exe .
```

Dependency: `github.com/Microsoft/go-winio` only.

## Architecture

```
CLI (deckpilot.exe)
├── send / show / list / launch / watch / auto-approvals
├── hang-detect / notify / wait-idle / cleanup / shutdown
└── Daemon (\\.\pipe\deckpilot-daemon, ws://127.0.0.1:8080/ws)
    ├── Session Discovery (.session files + parallel process probing)
    ├── Per-session Watcher (500ms polling, status tracking, IsHungAppWindow)
    ├── Idle Hooks (~/.deckpilot/idle-hooks/, persisted across daemon restarts)
    └── IPC handlers (SEND / SHOW / LIST / PING) over pipe + JSON over WS
```

- **Daemon**: Singleton process. Auto-starts on first command execution.
- **Watcher**: 1 goroutine per session. Serializes all pipe I/O to prevent races.
- **IPC**: Pipe-delimited, Base64-encoded protocol over `\\.\pipe\deckpilot-daemon`. WebSocket mirror at `ws://127.0.0.1:8080/ws`.
- **Discovery**: ToolHelp32 enumerates `ghostty.exe` PIDs, then probes them concurrently — one goroutine per PID — so a single stuck ghostty cannot push `LIST` past the daemon's 10s IPC deadline.

## Commands

### Responsibility Matrix

Each command has a single, well-defined responsibility:

| Concern                       | `show` | `watch` | `auto-approvals` | `hang-detect` |
|-------------------------------|--------|---------|------------------|---------------|
| Single session detail         | Yes    | -       | -                | -             |
| Multi-session overview        | -      | Yes     | -                | -             |
| Auto-send Enter on approval   | -      | -       | Yes              | -             |
| Detect hang + dump evidence   | -      | -       | -                | Yes           |

- **`show`** — read a specific session's buffer (view-only).
- **`watch`** — live dashboard of all sessions and their approval status (view-only).
- **`auto-approvals`** — send Enter automatically on approval prompts.
- **`hang-detect`** — out-of-band non-destructive hang monitor; auto-snapshots evidence to disk when a session hangs.

### `send` — Send a message

```bash
deckpilot send <session> <message...>
```

Sends text + Enter to the specified session. Compares buffer hash before and after sending; retries automatically if not reflected.

- Pauses the Watcher before queuing the command (prevents pipe I/O races).
- Slash commands (`/`-prefixed) send Enter twice to bypass TUI autocomplete.
- Automatically reverses MSYS2/Git Bash path conversion.

### `show` — Get session buffer (view-only)

```bash
deckpilot show [session] [--tail N] [--history] [--follow]
```

Retrieves an individual session's buffer. **Never sends input or performs auto-approval.**
For auto-approval, use `deckpilot auto-approvals <session>`.

- Session name optional: auto-selects the last session used by this caller.
- `--tail N`: Show last N lines (default: 50).
- `--history`: Retrieve full scrollback instead of live buffer.
- `--follow` / `-f`: Poll every 2 seconds and print updates. Display-only — no approval.

### `list` / `ls` — List sessions

```bash
deckpilot list
```

```
NAME        RUNTIME   STATUS
claude-01   winui3    idle
codex-02    wt        active
ghostty-39   winui3   stalled
```

`stalled` indicates the daemon's `IsHungAppWindow(hwnd)` probe returned TRUE — the OS-authoritative hang signal that `hang-detect` consults as its fast path.

### `launch` — Start an agent

```bash
deckpilot launch <agent> <prompt...> [--cwd DIR] [--no-meta-prompt]
```

Starts a new Ghostty window, waits for the agent to be ready, then sends the prompt. Prints the session name to stdout for use in scripts.

| Agent  | Command                                  | Ready string |
|--------|------------------------------------------|--------------|
| claude | `claude --dangerously-skip-permissions`  | `>`          |
| sonnet | `claude --model sonnet`                  | `>`          |
| haiku  | `claude --model haiku`                   | `>`          |
| codex  | `codex --full-auto`                      | `›`          |
| gemini | `gemini`                                 | `>`          |

#### Prompt persistence and `--no-meta-prompt`

By default, `deckpilot launch` records the prompt verbatim in two places so a hung session can be reconstructed without operator spelunking:

- `~/.deckpilot/launch-meta/<session>.json` — the `prompt` field (read by hang-detect's snapshot action to synthesise a `# resume_command:` line).
- `~/.deckpilot/hang-dumps/<ts>-<session>.log` — the `# original_prompt:` and `# resume_command:` lines, only written when the session actually hangs.

Both files are created with mode `0o600` so other accounts on shared POSIX hosts cannot read them. NTFS does not derive ACLs from the Unix mode bits, so on Windows the mode is intent rather than enforcement — protect the home directory through normal Windows ACLs if untrusted users share the box.

If the prompt itself contains tokens / API keys / other secrets, pass `--no-meta-prompt` to suppress prompt persistence entirely:

```bash
deckpilot launch claude --no-meta-prompt "deploy with $TOKEN"
```

When the flag is set:

- The launch-meta JSON stores `"prompt": ""` and `"redacted": true` — the prompt body never reaches disk.
- A subsequent hang-dump emits `# original_prompt: <redacted>` and a `# resume_command:` whose prompt argument is the literal placeholder `<REDACTED — supply manually>`. The operator must retype the real prompt when respawning.

### `watch` — Monitor sessions (view-only)

```bash
deckpilot watch [session] [--once] [--json]
```

Displays a live dashboard of all active sessions. **Does not send Enter or perform any approval.**
For auto-approval, use `deckpilot auto-approvals <session>`.

The `PENDING` column shows which sessions are awaiting approval input:

```
NAME              STATUS   PENDING              LAST-CHANGE  TAIL
claude-01         active   YES(Y/n)             15:04:03     "Allow edit to foo.ts? (Y/n)"
codex-02          idle     -                    15:03:45     "› ready"
```

Flags:
- `--once`: Take a single snapshot and exit (useful in scripts).
- `--json`: Output JSON Lines for machine consumption.

**Deprecated alias**: `deckpilot watch <session>` (with a session name argument) now emits a deprecation warning and runs in monitor-only mode. Use `deckpilot auto-approvals <session>` for auto-approval behavior. Suppress the warning with `DECKPILOT_SUPPRESS_DEPRECATION=1`.

### `auto-approvals` — Auto-approve prompts (alias: `approve`)

```bash
deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose]
```

Monitors a session and automatically sends Enter when an approval prompt is detected. **This is the only command that sends Enter on its own.**

Detection patterns:
- `Action Required`
- `Enter to select`
- `Y/n`
- `Allow`
- `trust`
- `Waiting`

Flags:
- `--interval <duration>`: Polling interval (default: `2s`). Examples: `1s`, `500ms`.
- `--dry-run`: Detect prompts and log them, but **do not send Enter**.
- `--verbose`: Log the matched pattern name (e.g., `Prompt detected (matched: "Y/n")`).

Stop with Ctrl+C.

### `hang-detect` — External non-destructive hang monitor

```bash
deckpilot hang-detect <session> \
  [--cpu-threshold N]      # default 1.0  (percent)
  [--stall-seconds N]      # default 60
  [--probe-interval DUR]   # default 5s
  [--sample-interval DUR]  # default 500ms (CPU sample window)
  [--on-hang ACTION]       # default snapshot
  [--include-children | --no-include-children]
  [--once]                 # one probe and exit (diagnostic)
```

Runs as a separate process alongside the daemon. The daemon owns infrastructure and per-session status; `hang-detect` enforces a specific health policy on a single session and reacts when it trips.

Hang signal sources, in order of authority:

1. **OS fast path** — daemon's watcher polls `IsHungAppWindow(hwnd)` and surfaces `status: "stalled"` in `LIST`. `hang-detect` consults this first; if the OS already says hung, no heuristic is needed.
2. **Heuristic path** — root process tree below `--cpu-threshold` percent CPU AND no buffer change for `--stall-seconds`.

Available `--on-hang` actions (all strictly non-destructive — no kill is exposed):

| Action     | Behaviour                                                                 |
|------------|---------------------------------------------------------------------------|
| `notify`   | Log to stderr and fire idle hooks.                                        |
| `snapshot` | Dump PTY tail + full history + resume context to `~/.deckpilot/hang-dumps/`. (Default.) |
| `ctrl-c`   | Send SIGINT to the agent (soft recovery, agent may resume on its own).    |
| `tiered`   | `notify` → `snapshot` → `ctrl-c` in sequence so evidence is preserved before the interrupt changes screen state. |

`Stop-Process` is one shell invocation away if termination is truly required; deckpilot intentionally does not automate it because killing a hung agent loses its reasoning chain and queued input.

Live evidence (today): `~/.deckpilot/hang-dumps/20260426-203735-ghostty-39964.log` and `…-211445-ghostty-39796.log` were emitted automatically when those sessions stuck on `winshot.exe`.

#### Hang dump format (snapshot v2)

Each dump is a plain-text file at `~/.deckpilot/hang-dumps/<YYYYMMDD-HHMMSS>-<session>.log`, mode `0o600`, with this header followed by buffer tail and full history:

```
# deckpilot hang snapshot v2
# session: ghostty-39964
# root_pid: 39964
# process_count: 7
# cpu_percent: 0.10
# stall_since: 2026-04-26T20:36:12+09:00
# detected_at: 2026-04-26T20:37:35+09:00
# inferred_agent: claude
# app_runtime: winui3
# uptime: 41m12s
# status: stalled
# pipe_path: \\.\pipe\ghostty-winui3-ghostty-39964-39964
# launched_agent: claude
# launched_cwd: C:\Users\yuuji\deckpilot
# launched_at: 2026-04-26T19:56:23+09:00
# original_prompt: <one-line preview of the prompt, or `<redacted>` when --no-meta-prompt was set>
# resume_command: deckpilot launch claude "<full prompt>" --cwd "C:\Users\yuuji\deckpilot"
# --- buffer tail (200 lines) ---
…
# --- full history (HISTORY|0) ---
…
```

For sessions started outside `deckpilot launch`, the launch-metadata block is replaced by `# launched_by: external` and a `# resume_hint:` pointing the operator at the buffer tail.

### `notify` — Idle notification hooks

```bash
deckpilot notify <add|remove|list> [args]
```

Registers persistent hooks fired by the daemon when a session transitions from `active` to `idle`. Hooks survive daemon restarts (`~/.deckpilot/idle-hooks/`).

Hook types:

| Type                                    | Action                                          |
|-----------------------------------------|-------------------------------------------------|
| `stdout [message]`                      | Print to stdout when the session goes idle.     |
| `http <url> [method] [headers...]`      | Send HTTP request (default `POST`).             |
| `send <target_session> [message]`       | Send a message to another session.              |

Each hook accepts `--session <name>` to scope it to one session; without the flag it fires for every session.

```bash
deckpilot notify add stdout "Task completed"
deckpilot notify add http https://hooks.slack.com/webhook POST
deckpilot notify add send main-session "Background task finished" --session ghostty-39964
deckpilot notify list
deckpilot notify remove          # removes all (currently bulk-only)
```

### `wait-idle` — Block until a session goes idle

```bash
deckpilot wait-idle <session> [--timeout=DUR] [--poll=DUR]
```

Blocks the calling process until the named session transitions to `idle`. Pairs naturally with background-task push notifications:

```bash
deckpilot launch claude "long-running refactor"
deckpilot wait-idle <session> && curl -X POST $SLACK_HOOK -d '{"text":"done"}'
```

### `cleanup` — Sweep on-disk artefacts

```bash
deckpilot cleanup [--days N] [--dry-run]
```

Sweeps two directories under `~/.deckpilot/` with deliberately different retention policies in one pass:

| Directory                       | Policy                                                                                   |
|---------------------------------|------------------------------------------------------------------------------------------|
| `~/.deckpilot/hang-dumps/`      | **Age-based.** Default `--days 3`. These are operational evidence; the value is the audit trail, not session liveness. |
| `~/.deckpilot/launch-meta/`     | **Liveness-based.** A meta file is deleted only if its session is no longer in the daemon's `LIST`. Age is irrelevant — a long-running session legitimately keeps its meta for weeks. If the daemon is unreachable, the launch-meta sweep is a conservative no-op (issue #29). |
| `~/.deckpilot/idle-hooks/`      | **Not swept.** User configuration registered via `deckpilot notify add`; lifecycle is exclusively `deckpilot notify remove` (issue #31). |

Recommended: add `deckpilot cleanup` to a daily scheduled task or shell profile.

### `shutdown` — Stop the daemon

```bash
deckpilot shutdown
```

Asks the singleton daemon to exit cleanly. The next deckpilot command will auto-spawn a fresh one.

### `version`

```bash
deckpilot version
```

Prints the embedded version, commit, and build time.

## Caller Identification

`show` can omit the session name because deckpilot identifies the calling terminal automatically. Resolution priority:

1. `DECKPILOT_CALLER` environment variable (explicit override).
2. `WT_SESSION` (Windows Terminal tab/pane GUID) → `wt:<guid>`.
3. `GHOSTTY_SESSION` → `ghostty:<guid>`.
4. PPID (parent process ID) → `pid:<ppid>`.
5. `default` fallback.

Each `send` records `lastUsed[caller] = session`; subsequent `show` calls without an argument reuse it.

## Session Discovery

Two-stage fallback:

### 1. `.session` files (preferred)

```
%LOCALAPPDATA%\ghostty\control-plane\{winui3,win32,web}\sessions\*.session
%LOCALAPPDATA%\WindowsTerminal\control-plane\winui3\sessions\*.session
```

`key=value` records carrying `session_name`, `pipe_path`, `pid`, `hwnd`. The daemon validates each file by checking process liveness and pipe responsiveness; invalid files are auto-deleted. Windows Terminal entries are produced by `wt-sidecar`.

### 2. Process probing (fallback)

Windows ToolHelp32 enumerates `ghostty.exe` and probes the canonical pipe name `\\.\pipe\ghostty-{runtime}-ghostty-<pid>-<pid>`. Probes run **concurrently** (one goroutine per PID), so a single stuck ghostty cannot block the rest of the sweep — see *Recent Performance Improvements* below.

## Known Issues & Workarounds

### Ghostty CP drain race
**Symptom**: After sending a message, Enter is dropped and the buffer is not updated.
**Mitigation**: Hash the buffer before and after; if it has not changed within 200ms, retry `\r` up to 3 times.

### Slash-command autocomplete
**Symptom**: For `/`-prefixed commands, the first Enter is consumed by the TUI autocomplete menu.
**Mitigation**: When the message starts with `/`, send a second Enter 200ms later.

### TUI agent idle detection
**Symptom**: TUI agents (Claude Code etc.) constantly redraw cursor blink and statusbar, so the buffer never stabilises into `idle`.
**Mitigation**: `launch` does not wait for `idle`; it waits for an agent-specific Ready string (`>` or `›`).

### Legacy `RAW_INPUT` rejection
**Symptom**: Older Ghostty builds reject the `RAW_INPUT` IPC verb.
**Mitigation**: On `PARSE_ERROR`, fall back to `INPUT` (text-encoded).

### MSYS2 / Git Bash path conversion
**Symptom**: Slash-prefixed arguments are silently rewritten by Git Bash (e.g. `/help` → `C:/Program Files/Git/help`).
**Mitigation**: Detect known prefixes and reverse the conversion before writing to the pipe.

### `ghostty -e <cmd>` flake
**Symptom**: Debug builds of Ghostty crash the IO thread when launched with `-e`.
**Mitigation**: Launch `ghostty` with no args, wait for the shell prompt, then deliver the command over the pipe.

## Recent Performance Improvements

**2026-04-26 — Parallel discovery (`aef6646`).** `pipe.Discover`, called by the daemon's `LIST` handler and by `cmd/launch`'s `waitForNewSession`, used to probe every ghostty PID **serially**. Each probe carried `dialTimeout=2s` + `readDeadline=5s`, so two stuck ghostty processes were enough to push `refreshSessions` past the 10s IPC handler deadline; clients then saw `list: no response from daemon` and the 15s `waitForNewSession` budget routinely missed freshly-launched sessions.

`probePIDsConcurrent` now fans out one goroutine per PID and preserves PID-input order so callers stay deterministic. Live smoke after the fix: `deckpilot ls` returns in **36ms** against the rebuilt binary, even with a previously-blocking ghostty in the set.

Coverage: `pipe/discovery_concurrent_test.go` exercises parallelism, slow-probe isolation, failure dropping, and the empty-input fast path. Full suite runs green under `-race`. Background: `logs/2026-04-26_bugfix.md`.

## IPC Protocol

The daemon listens on `\\.\pipe\deckpilot-daemon`:

| Command | Wire format                                  | Response                                  |
|---------|----------------------------------------------|-------------------------------------------|
| `PING`  | `PING`                                       | `PONG`                                    |
| `SEND`  | `SEND\|<name>\|<base64msg>\|<caller>`        | `OK\|ack\|<id>` or `OK\|sent\|no_ack`     |
| `LIST`  | `LIST`                                       | `OK\|<json>`                              |
| `SHOW`  | `SHOW\|<name>\|<mode>\|<caller>`             | `OK\|<base64content>\|<status>`           |
| `TAIL`  | `TAIL\|<lines>` (sent to the session pipe)   | streamed buffer tail                      |
| `HISTORY` | `HISTORY\|0` (sent to the session pipe)    | full scrollback                           |

## WebSocket Endpoint

The daemon also exposes a JSON WebSocket mirror.

- **URL**: `ws://127.0.0.1:8080/ws` (Localhost ONLY)
- **Flag**: `deckpilot daemon --ws-port <N>` (default `8080`, `0` to disable)
- **Protocol**: One JSON object per frame.

### Commands (Client → Server)

| Cmd      | Fields                                  | Description                       |
|----------|-----------------------------------------|-----------------------------------|
| `INPUT`  | `session`, `msg` (base64), `from`       | Send message + Enter              |
| `SHOW`   | `session`, `mode` (`buffer`/`history`)  | Get session buffer                |
| `STATE`  | `session`                               | Get session status                |
| `LIST`   | (none)                                  | List active sessions              |
| `PING`   | (none)                                  | Connection heartbeat              |

### Responses (Server → Client)

Always a JSON object with `cmd`, `ok` (bool), and a payload.

```json
{ "cmd": "LIST", "ok": true,
  "data": [ { "name": "ghostty-1234", "status": "idle" } ] }
```

```json
{ "cmd": "INPUT", "ok": false, "error": "session not found: foo" }
```

## License

MIT
