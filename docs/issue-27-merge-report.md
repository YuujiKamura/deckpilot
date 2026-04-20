# Issue #27 WS Merge Report

Branch: `fix/issue-27-merge-ws-bridges` (branched from `feat/issue-27-ws-endpoint`)

## 削除したファイル

- `daemon/ws.go` (134 行) — `Status` ベースの旧 WebSocket ブリッジ。4 コマンド (PING/LIST/SEND/SHOW) のみサポートしていた。
  - `WSMessage` / `WSResponse` / `ServeWS` / `handleWS` / `upgrader` / `parseIPCResponse` / `sendWSError` がすべて `daemon/wsserver.go` に重複定義されており、両ファイル同居では `duplicate symbol` で `go build` が失敗する状態だった。
  - `wsserver.go` は旧 4 コマンド + INPUT + STATE を扱う上位互換で、Ok/Error 両フィールドに加え Status も保持する。

## wsserver.go に吸収した ws.go の機能

既に `wsserver.go` が `ws.go` の全機能を包含しており、追加移植は発生しなかった。確認事項:

- PING 応答: `Ok: true, Message: "PONG"` (旧 `Status: "OK", Message: "PONG"` 相当)
- LIST 応答: `Ok: true, Data: sessions`
- SEND / INPUT: `handleSend(parts)` → `parseIPCResponse` 経由でマッピング (INPUT は SEND のエイリアスとして扱う)
- SHOW: `handleShow(parts)` → `parseIPCResponse`、`Data` に base64 コンテンツ、`Status` に watcher の idle/active を伝播
- unknown cmd: `Ok: false, Error: "unknown cmd: ..."` (旧 `Status: "ERR", Message: "Unknown command: ..."` 相当)
- `sendWSError` シグネチャ拡張: `(conn, cmd, msg)` で Cmd フィールドを埋める

consumer 側で `Status` の idle/active/dead 表示を期待するコードがあるため、`WSResponse.Status` フィールドは保持。`Ok` フィールドは test-client / photo-ai-lisp cp-client / README 契約により必須。

## 追加したテストと通過状況

`daemon/wsserver_test.go` に 2 件追加:

- `TestWS_INPUT_round_trip`: INPUT コマンドを nonexistent session に送り、`Cmd="INPUT"`, `Ok=false`, `Error` に "session not found" が入ることを確認。INPUT コマンドのディスパッチ経路と envelope 整合を担保。
- `TestWS_SHOW_round_trip`: mock watcher (status=dead, lastContent="mock buffer content") をインストールして SHOW を送り、`Cmd="SHOW"`, `Ok=true`, `Mode="buffer"`, `Status="dead"`, `Data` は base64("mock buffer content") を検証。`OK|<base64>|<status>` IPC 文字列が正しく Ok/Data/Status にマップされることを担保。

`go test ./...` 結果 (全 4 パッケージ):

```
ok      github.com/YuujiKamura/deckpilot        5.639s
ok      github.com/YuujiKamura/deckpilot/cmd    (cached)
?       github.com/YuujiKamura/deckpilot/cmd/stressghostty      [no test files]
ok      github.com/YuujiKamura/deckpilot/daemon 4.152s
ok      github.com/YuujiKamura/deckpilot/pipe   (cached)
```

`go vet ./...` 無警告。

## 更新した consumer (html/docs)

- `test-client.html`: 変更なし。レスポンスを `JSON.stringify` でダンプするのみで、旧 `data.status === "OK"` 相当の分岐は持っていなかったため Ok ベース API への移行透過。
- `README.md`: feat/issue-27-ws-endpoint が既に Ok/Error shape で記述済み。`Status` 分岐は残存せず、変更不要。
- `docs/issue-27-ws-babysit-report.md`: 旧 `ws.go` の rename 経緯を記録した過去ログ。履歴として保持。

## 残る懸念

1. **INPUT happy path の統合テスト欠落**: `handleSend` の正常系は Windows named pipe + watcher goroutine + ghostty-winui3 プロセスが揃った状態でなければ通らず、ユニットテスト化できない。現状はマニュアル検証 (test-client.html で INPUT 送信) と、Issue #25 Phase2 の end-to-end テストに依存している。CI で回す場合は mock pipe server を `pipe` パッケージ側に用意する必要がある。
2. **INPUT と SEND のエイリアス関係**: `wsserver.go` では `case "INPUT", "SEND"` で同一ハンドラを呼んでいる。将来 SEND を deprecate する場合、consumer (photo-ai-lisp cp-client 等) の移行状況を確認してから段階的に切る必要がある。
