# [HUB-DISPATCH] Issue #27: WebSocket control endpoint

## プロジェクト
C:/Users/yuuji/deckpilot

## 参照
- Issue: https://github.com/YuujiKamura/deckpilot/issues/27
- 本文は `gh issue view 27` で取得せよ
- 初回クライアントは `C:/Users/yuuji/photo-ai-lisp` の `src/cp-client.lisp` +
  `src/cp-protocol.lisp`。同じ JSON 形状を使う想定なのでこちらも参照

## ゴール
deckpilot daemon に WebSocket エンドポイント (`ws://127.0.0.1:<port>/ws`) を
追加し、既存 CLI (`send/show/list`) と同等のコマンドを JSON over WS で
受け付けられるようにする。

## ブランチ
`main` から `feat/issue-27-ws-endpoint` を切って作業。push は禁止、commit
のみ。完了時 `DISPATCH-DONE` を出して停止。

## 作業手順

### 1. 既存 daemon 構造の把握
- `cmd/` / `daemon/` / `pkg/` あたりの Go ファイルを grep:
  `ListenAndServe|net.Listen|handler|socket|command`
- 既存 UNIX socket handler と同じ内部関数を呼び出す構造にする

### 2. 依存追加
- `go get github.com/gorilla/websocket` （既存 go.mod に追記）
- stdlib だけで書ける場合はそれでも良い (`golang.org/x/net/websocket` は
  deprecated 気味なので避ける)

### 3. WS サーバ実装
- 新規ファイル: `daemon/wsserver.go` (既存レイアウトに従う)
- `StartWSServer(addr string) error` 相当の関数
- `http.Handler` で `/ws` を登録、`http.Server` で `127.0.0.1:<port>` に bind
- `0.0.0.0` や `:<port>` (全インタフェース) は**絶対に bind しない**

### 4. コマンドディスパッチ
- 受信 JSON の `cmd` を見て INPUT / SHOW / STATE / LIST に分岐
- 既存 CLI ハンドラの内部関数を呼び出してレスポンスを構築
- 未知 `cmd` → `{"cmd":"<name>","ok":false,"error":"unknown cmd: ..."}`
- `msg` は base64 デコードしてから子プロセスに渡す

### 5. フラグ追加
- daemon 起動フラグに `--ws-port <N>` (デフォルト 8080、0 で無効)
- 既存 daemon のフラグ定義箇所に追加

### 6. テスト
- `daemon/wsserver_test.go` を書く。Go 標準の `httptest.NewServer` +
  `gorilla/websocket.Dialer` で
  - 4 コマンド全部の往復テスト
  - 未知 cmd のエラーテスト
  - localhost 以外に bind していないことの確認
- 既存テストを壊していないことを `go test ./...` で確認

### 7. CLI 動作確認
- `deckpilot list/send/show` が従来通り動くこと (新規機能が既存を壊さない)

### 8. ドキュメント
- `README.md` に WS エンドポイントの短いセクション追加 (5〜15行)
- localhost 限定であることを明記
- サンプル: `websocat ws://127.0.0.1:8080/ws` → `{"cmd":"LIST"}` で応答

### 9. 成果物
- commit 3〜5 本程度に分割 (feat + test + docs):
  - `feat(ws): add gorilla/websocket dependency`
  - `feat(ws): add /ws endpoint with INPUT/SHOW/STATE/LIST dispatch`
  - `test(ws): round-trip tests for all 4 commands`
  - `docs(ws): README section for WebSocket endpoint`

### 10. 完了
- `DISPATCH-DONE` を出力して停止

## 制約 (破るな)
1. `git push` 一切禁止
2. origin への反映禁止
3. `main` への直接 commit 禁止 (必ず feat ブランチで)
4. `0.0.0.0` bind 禁止、localhost 限定
5. 認証は入れない (localhost 前提、UNIX socket と parity)
6. 既存 `send/show/list/launch/...` の動作を壊すな
7. scope creep 禁止 (streaming PTY output, TLS, auth は別 issue)
8. `gh issue comment` / `gh pr create` 禁止 (人間判断)

## 参考
- 他の daemon 系 issue: #23 (buffer-hash submit detect), #25 (auto-approve PTY)
- photo-ai-lisp 側 cp-protocol.lisp の JSON 形状と整合すること
