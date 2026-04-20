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

### `send` — Send a message

```bash
deckpilot send <session> <message...>
```

Sends text + Enter to the specified session. Compares buffer hash before and after sending; retries automatically if not reflected.

- Pauses the Watcher before queuing the command (prevents pipe I/O races).
- Slash commands (`/`-prefixed) send Enter twice to bypass TUI autocomplete.
- Automatically reverses MSYS2/Git Bash path conversion.

### `show` — Get session buffer (detail, view-only)

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
```

### `launch` — Start an agent

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

`show` でセッション名を省略できるよう、呼び出し元ターミナルを自動識別する。

**優先順位:**
1. `DECKPILOT_CALLER` 環境変数（明示指定）
2. `WT_SESSION` — Windows Terminal のタブ/ペイン GUID → `wt:<guid>`
3. `GHOSTTY_SESSION` → `ghostty:<guid>`
4. PPID（親プロセス ID）→ `pid:<ppid>`
5. `default` フォールバック

`send` 実行時に `lastUsed[caller] = session` を記録し、以降の `show` で自動解決する。

## セッション検出

2 段階のフォールバック:

### 1. .session ファイル（優先）

```
%LOCALAPPDATA%\ghostty\control-plane\{winui3,win32,web}\sessions\*.session
%LOCALAPPDATA%\WindowsTerminal\control-plane\winui3\sessions\*.session
```

key=value 形式で `session_name`, `pipe_path`, `pid`, `hwnd` を保持。プロセス生存と pipe 応答を検証し、不正なファイルは自動削除。Windows Terminal の場合は `wt-sidecar` がこのファイルを生成する。

### 2. プロセス探索（フォールバック）

Windows ToolHelp32 API で `ghostty.exe` を列挙し、既知のパイプ命名規則 `\\.\pipe\ghostty-{runtime}-ghostty-<pid>-<pid>` を探索する。

## 既知の問題と対策

### Ghostty CP ドレイン問題

**症状**: メッセージ送信後、Enter が消失しバッファに反映されない。

**対策**: 送信前後のバッファハッシュを比較し、300ms 経過後も変化がなければ `\r` を最大 3 回リトライする。

### スラッシュコマンドのオートコンプリート

**症状**: `/help` 等のスラッシュコマンドで最初の Enter がオートコンプリートメニューに吸収される。

**対策**: `/` 始まりのメッセージ検出時、300ms 後に 2 回目の Enter を自動送信。

### TUI エージェントの idle 検出

**症状**: Claude Code 等の TUI エージェントはカーソル点滅やステータスバー更新で常にバッファが変化し、`idle` 状態に安定しない。

**対策**: `launch` では `idle` を待たず、エージェント固有の Ready 文字列（`>`, `›`）の出現のみで準備完了を判定。

### RAW_INPUT 非対応

**症状**: 古い Ghostty ビルドが `RAW_INPUT` コマンドを認識しない。

**対策**: `PARSE_ERROR` 応答時に `INPUT`（テキストエンコード）へ自動フォールバック。

### MSYS2 パス変換

**症状**: Git Bash 上で引数のスラッシュが Windows パスに変換される（例: `/help` → `C:/Program Files/Git/help`）。

**対策**: 既知のプレフィックスを検出し、元のパスに逆変換する。

### Ghostty -e オプション

**症状**: `ghostty -e <cmd>` がデバッグビルドで IO スレッドエラーを起こす。

**対策**: Ghostty を引数なしで起動し、シェルプロンプト出現後にコマンドを pipe 経由で入力する。

## IPC プロトコル

Daemon は `\\.\pipe\deckpilot-daemon` で以下のコマンドを受け付ける:

| コマンド | 形式 | 応答 |
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

### Commands (Client → Server)

| Cmd | Fields | Description |
|-----|--------|-------------|
| `INPUT` | `session`, `msg` (base64), `from` | Send message + Enter |
| `SHOW` | `session`, `mode` (buffer/history) | Get session buffer |
| `STATE` | `session` | Get session status |
| `LIST` | (none) | List active sessions |
| `PING` | (none) | Connection heartbeat |

### Responses (Server → Client)

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
