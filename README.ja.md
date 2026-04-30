# deckpilot

Ghostty ターミナル上の AI エージェント（Claude Code, Codex, Gemini）をプログラマブルに操作する CLI ツール。Named Pipe 経由でセッション検出・メッセージ送信・バッファ取得・自動承認を行う。

## 前提条件

- **Ghostty Windows版** ([ghostty-win](https://github.com/YuujiKamura/ghostty-win)) または **Windows Terminal** (要 `wt-sidecar` 連携) が必要。
- Go 1.21+
- Windows 10/11

## インストール

```bash
go build -o deckpilot.exe .
```

依存: `github.com/Microsoft/go-winio` のみ。

## アーキテクチャ

```
CLI (deckpilot.exe)
├── send / show / list / launch / watch / auto-approvals
└── Daemon (\\.\pipe\deckpilot-daemon)
    ├── Session Discovery (.session files + process probing)
    ├── Per-session Watcher (500ms polling, status tracking)
    └── IPC handlers (SEND / SHOW / LIST / PING)
```

- **Daemon**: シングルトンプロセス。初回コマンド実行時に自動起動。
- **Watcher**: セッションごとに 1 goroutine。全 pipe I/O を直列化し競合を防止。
- **IPC**: `\\.\pipe\deckpilot-daemon` 上のパイプ区切り・Base64 エンコードプロトコル。

## コマンド

### 責務マトリクス

各コマンドは単一の責務を持つ:

| 関心事                        | `show` | `watch` | `auto-approvals` |
|-------------------------------|--------|---------|------------------|
| 単一セッション詳細             | Yes    | -       | -                |
| 複数セッション俯瞰             | -      | Yes     | -                |
| 承認プロンプトへの Enter 自動送信 | -    | -       | Yes              |

- **`show`**: 特定セッションのバッファを読みたいとき。
- **`watch`**: 全セッションのライブダッシュボードと承認待ち状態を把握したいとき。
- **`auto-approvals`**: 承認プロンプトに自動で Enter を送りたいとき。

### `send` — メッセージ送信

```bash
deckpilot send <session> <message...>
```

指定セッションにテキスト + Enter を送信する。送信前後のバッファハッシュを比較し、未反映なら自動リトライする。

- Watcher を一時停止してからコマンドキューに投入（pipe I/O 競合防止）
- スラッシュコマンド (`/` 始まり) は TUI オートコンプリート対策で Enter を 2 回送信
- MSYS2/Git Bash のパス変換を自動逆変換

### `show` — バッファ取得（個別詳細・表示専用）

```bash
deckpilot show [session] [--tail N] [--history] [--follow]
```

個別セッションのバッファを取得する。**入力送信や自動承認は一切行わない。**
自動承認が必要な場合は `deckpilot auto-approvals <session>` を使うこと。

- セッション名省略時: caller の最後に使ったセッションを自動選択。
- `--tail N`: 末尾 N 行を表示（デフォルト: 50）。
- `--history`: ライブバッファの代わりにスクロールバック全体を取得。
- `--follow` / `-f`: 2 秒ごとに再取得して追記表示。表示専用 — 承認処理なし。

### `list` / `ls` — セッション一覧

```bash
deckpilot list
```

```
NAME        RUNTIME   STATUS
claude-01   winui3    idle
codex-02    wt        active
```

### `launch` — エージェント起動

```bash
deckpilot launch <agent> <prompt...> [--cwd DIR]
```

Ghostty ウィンドウを新規起動し、エージェントの準備完了を待ってからプロンプトを送信する。セッション名を stdout に出力するのでスクリプトから利用可能。

| Agent   | コマンド                                  | Ready 文字列 |
|---------|------------------------------------------|--------------|
| claude  | `claude --dangerously-skip-permissions`  | `>`          |
| sonnet  | `claude --model sonnet`                  | `>`          |
| haiku   | `claude --model haiku`                   | `>`          |
| codex   | `codex --full-auto`                      | `›`          |
| gemini  | `gemini`                                 | `>`          |

### `watch` — セッション監視（表示専用）

```bash
deckpilot watch [session] [--once] [--json]
```

全アクティブセッションのライブダッシュボードを表示する。**Enter 送信や承認処理は一切行わない。**
自動承認が必要な場合は `deckpilot auto-approvals <session>` を使うこと。

`PENDING` カラムでどのセッションが承認待ちかを即時把握できる:

```
NAME              STATUS   PENDING              LAST-CHANGE  TAIL
claude-01         active   YES(Y/n)             15:04:03     "Allow edit to foo.ts? (Y/n)"
codex-02          idle     -                    15:03:45     "› ready"
```

フラグ:
- `--once`: スナップショットを 1 回出力して終了（スクリプト用）。
- `--json`: JSON Lines 形式で出力（機械可読）。

**非推奨エイリアス**: `deckpilot watch <session>`（セッション名引数あり）は非推奨警告を出したうえで監視専用モードで動作する。自動承認が必要な場合は `deckpilot auto-approvals <session>` を使うこと。警告を抑制するには `DECKPILOT_SUPPRESS_DEPRECATION=1` を設定する。

### `auto-approvals` — 自動承認（エイリアス: `approve`）

```bash
deckpilot auto-approvals <session> [--interval 2s] [--dry-run] [--verbose]
```

セッションを監視し、承認プロンプトを検出したら自動で Enter を送信する。**Enter を送信するのはこのコマンドのみ。**

検出パターン:
- `Action Required`
- `Enter to select`
- `Y/n`
- `Allow`
- `trust`
- `Waiting`

フラグ:
- `--interval <duration>`: ポーリング間隔（デフォルト: `2s`）。例: `1s`, `500ms`。
- `--dry-run`: プロンプト検出とログ出力のみ行い、**Enter は送信しない**。
- `--verbose`: 検出パターン名もログに出力する（例: `Prompt detected (matched: "Y/n")`）。

Ctrl+C で停止。

## Caller 識別

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

## License

MIT
