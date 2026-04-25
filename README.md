# deckpilot

A CLI tool for programmatic control of AI agents (Claude Code, Codex, Gemini) running in Ghostty terminal. Provides session discovery, message sending, buffer retrieval, and auto-approval via Named Pipe.

## Prerequisites

- **Ghostty for Windows** ([ghostty-win](https://github.com/YuujiKamura/ghostty-win)) or **Windows Terminal** (requires `wt-sidecar`) is needed.
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
└── Daemon (\\.\pipe\deckpilot-daemon)
    ├── Session Discovery (.session files + process probing)
    ├── Per-session Watcher (500ms polling, status tracking)
    └── IPC handlers (SEND / SHOW / LIST / PING)
```

- **Daemon**: Singleton process. Auto-starts on first command execution.
- **Watcher**: 1 goroutine per session. Serializes all pipe I/O to prevent races.
- **IPC**: Pipe-delimited, Base64-encoded protocol over `\\.\pipe\deckpilot-daemon`.

## Commands

### Responsibility Matrix

Each command has a single, well-defined responsibility:

| Concern                       | `show` | `watch` | `auto-approvals` |
|-------------------------------|--------|---------|------------------|
| Single session detail         | Yes    | -       | -                |
| Multi-session overview        | -      | Yes     | -                |
| Auto-send Enter on approval   | -      | -       | Yes              |

- Use **`show`** when you need to read a specific session's buffer.
- Use **`watch`** when you want a live dashboard of all sessions and their approval status.
- Use **`auto-approvals`** when you want Enter sent automatically on approval prompts.

### `send`  ESend a message

```bash
deckpilot send <session> <message...>
```

Sends text + Enter to the specified session. Compares buffer hash before and after sending; retries automatically if not reflected.

- Pauses the Watcher before queuing the command (prevents pipe I/O races).
- Slash commands (`/`-prefixed) send Enter twice to bypass TUI autocomplete.
- Automatically reverses MSYS2/Git Bash path conversion.

### `show`  EGet session buffer (detail, view-only)

```bash
deckpilot show [session] [--tail N] [--history] [--follow]
```

Retrieves an individual session's buffer. **Never sends input or performs auto-approval.**
For auto-approval, use `deckpilot auto-approvals <session>`.

- Session name optional: auto-selects the last session used by this caller.
- `--tail N`: Show last N lines (default: 50).
- `--history`: Retrieve full scrollback instead of live buffer.
- `--follow` / `-f`: Poll every 2 seconds and print updates. Display-only  Eno approval.

### `list` / `ls`  EList sessions

```bash
deckpilot list
```

```
NAME        RUNTIME   STATUS
claude-01   winui3    idle
codex-02    wt        active
```

### `launch`  EStart an agent

```bash
deckpilot launch <agent> <prompt...> [--cwd DIR]
```

Starts a new Ghostty window, waits for the agent to be ready, then sends the prompt. Prints the session name to stdout for use in scripts.

| Agent  | Command                                  | Ready string |
|--------|------------------------------------------|--------------|
| claude | `claude --dangerously-skip-permissions`  | `>`          |
| sonnet | `claude --model sonnet`                  | `>`          |
| haiku  | `claude --model haiku`                   | `>`          |
| codex  | `codex --full-auto`                      | `›`          |
| gemini | `gemini`                                 | `>`          |

### `watch`  EMonitor sessions (view-only)

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

### `auto-approvals`  EAuto-approve prompts (alias: `approve`)

```bash
deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose]
```

Monitors a session and automatically sends Enter when an approval prompt is detected. **This is the only command that sends Enter.**

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

## Caller Identification

`show` でセチE��ョン名を省略できるよう、呼び出し�Eターミナルを�E動識別する、E
**優先頁E��E**
1. `DECKPILOT_CALLER` 環墁E��数�E��E示持E��！E2. `WT_SESSION`  EWindows Terminal のタチEペイン GUID ↁE`wt:<guid>`
3. `GHOSTTY_SESSION` ↁE`ghostty:<guid>`
4. PPID�E�親プロセス ID�E��E `pid:<ppid>`
5. `default` フォールバック

`send` 実行時に `lastUsed[caller] = session` を記録し、以降�E `show` で自動解決する、E
## セチE��ョン検�E

2 段階�Eフォールバック:

### 1. .session ファイル�E�優先！E
```
%LOCALAPPDATA%\ghostty\control-plane\{winui3,win32,web}\sessions\*.session
%LOCALAPPDATA%\WindowsTerminal\control-plane\winui3\sessions\*.session
```

key=value 形式で `session_name`, `pipe_path`, `pid`, `hwnd` を保持。�Eロセス生存と pipe 応答を検証し、不正なファイルは自動削除。Windows Terminal の場合�E `wt-sidecar` がこのファイルを生成する、E
### 2. プロセス探索�E�フォールバック�E�E
Windows ToolHelp32 API で `ghostty.exe` を�E挙し、既知のパイプ命名規則 `\\.\pipe\ghostty-{runtime}-ghostty-<pid>-<pid>` を探索する、E
## 既知の問題と対筁E
### Ghostty CP ドレイン問顁E
**痁E��**: メチE��ージ送信後、Enter が消失しバチE��ァに反映されなぁE��E
**対筁E*: 送信前後�Eバッファハッシュを比輁E��、E00ms 経過後も変化がなければ `\r` を最大 3 回リトライする、E
### スラチE��ュコマンド�EオートコンプリーチE
**痁E��**: `/help` 等�EスラチE��ュコマンドで最初�E Enter がオートコンプリートメニューに吸収される、E
**対筁E*: `/` 始まり�EメチE��ージ検�E時、E00ms 後に 2 回目の Enter を�E動送信、E
### TUI エージェント�E idle 検�E

**痁E��**: Claude Code 等�E TUI エージェント�Eカーソル点滁E��スチE�Eタスバ�E更新で常にバッファが変化し、`idle` 状態に安定しなぁE��E
**対筁E*: `launch` では `idle` を征E��ず、エージェント固有�E Ready 斁E���E�E�E>`, `›`�E��E出現のみで準備完亁E��判定、E
### RAW_INPUT 非対忁E
**痁E��**: 古ぁEGhostty ビルドが `RAW_INPUT` コマンドを認識しなぁE��E
**対筁E*: `PARSE_ERROR` 応答時に `INPUT`�E�テキストエンコード）へ自動フォールバック、E
### MSYS2 パス変換

**痁E��**: Git Bash 上で引数のスラチE��ュぁEWindows パスに変換される（侁E `/help` ↁE`C:/Program Files/Git/help`�E�、E
**対筁E*: 既知のプレフィチE��スを検�Eし、�Eのパスに送E��換する、E
### Ghostty -e オプション

**痁E��**: `ghostty -e <cmd>` がデバッグビルドで IO スレチE��エラーを起こす、E
**対筁E*: Ghostty を引数なしで起動し、シェルプロンプト出現後にコマンドを pipe 経由で入力する、E
## IPC プロトコル

Daemon は `\\.\pipe\deckpilot-daemon` で以下�Eコマンドを受け付けめE

| コマンチE| 形弁E| 応筁E|
|---------|------|------|
| PING | `PING` | `PONG` |
| SEND | `SEND\|<name>\|<base64msg>\|<caller>` | `OK\|ack\|<id>` or `OK\|sent\|no_ack` |
| LIST | `LIST` | `OK\|<json>` |
| SHOW | `SHOW\|<name>\|<mode>\|<caller>` | `OK\|<base64content>\|<status>` |

## WebSocket Endpoint

The daemon exposes a WebSocket control endpoint for external agents and tools.

- **URL**: `ws://127.0.0.1:8080/ws` (Localhost ONLY)
- **Flag**: `--ws-port <N>` (Default: 8080, `0` to disable)
- **Protocol**: One JSON object per frame.

### Commands (Client ↁEServer)

| Cmd | Fields | Description |
|-----|--------|-------------|
| `INPUT` | `session`, `msg` (base64), `from` | Send message + Enter |
| `SHOW` | `session`, `mode` (buffer/history) | Get session buffer |
| `STATE` | `session` | Get session status |
| `LIST` | (none) | List active sessions |
| `PING` | (none) | Connection heartbeat |

### Responses (Server ↁEClient)

Always returns a JSON object with `cmd`, `ok` (bool), and payload.

Example `LIST`:
```json
{
  "cmd": "LIST",
  "ok": true,
  "data": [{"name": "ghostty-1234", "status": "idle", ...}]
}
```

Example `INPUT` error:
```json
{
  "cmd": "INPUT",
  "ok": false,
  "error": "session not found: foo"
}
```

## License

MIT

## Hang Detection & Recovery (Issue #26)

Deckpilot uses a **decoupled monitoring architecture**. The daemon manages infrastructure, while hang-detect runs as a separate process to enforce specific health policies per session.

### Non-destructive Recovery Workflow

1.  **Monitor**: Start a monitor for a critical session.
    `powershell
    deckpilot hang-detect <session> --on-hang tiered-recover
    `
2.  **Detection**: If the session hangs (OS-level or heuristic), it captures a snapshot to ~/.deckpilot/hang-dumps/.
3.  **Interaction**: It prompts the user Suggesting recovery: launch a new Ghostty session? (y/N): .
4.  **Revival**: Upon y, it spawns a fresh Ghostty window, allowing the operator to resume work while keeping the hung process for post-mortem analysis.

### Available Actions
- 
otify: Log to stderr and fire idle hooks.
- snapshot: Dump PTY buffer to disk (Evidence preservation).
- ctrl-c: Send SIGINT to the agent (Soft recovery).
- 
ecover: Snapshot + User confirmation to spawn a new Ghostty.
- 	iered-recover: Notify + Snapshot + Recover.

### Maintenance & Cleanup Policy

Hang dumps are stored in ~/.deckpilot/hang-dumps/. To prevent disk bloat while preserving fresh evidence:

- **Default Policy**: We recommend a **3-day retention period**. Most hang causes are investigated immediately; older logs are likely stale.
- **Manual Cleanup**:
  `powershell
  # Delete dumps older than 3 days (default)
  deckpilot cleanup
  
  # Keep logs for a week if you're on a long-running task
  deckpilot cleanup --days 7
  `
- **Auto-Cleanup Recommendation**: Add deckpilot cleanup to your shell profile or a daily cron/scheduled task to keep the dump folder tidy.
