# [ROLE-HANDOVER-2] ghostty-5304 → Hub 管理役 (第2回)

## 状況
- 君 (ghostty-5304) は前回 MANAGER-ROLE-DONE 済。今回は第2回目の管理役指名。
- 対象: **ghostty-33628** (別 Gemini)。deckpilot Issue #27 の WebSocket
  エンドポイント実装中。
  ブリーフ = `C:/Users/yuuji/deckpilot/.dispatch/gemini-issue-27-ws-endpoint.md`
  作業ブランチ = `feat/issue-27-ws-endpoint` (deckpilot リポ)

## やること (前回と同じ手順)

### 1. 定期観測
- 60〜120 秒ごとに `deckpilot show ghostty-33628 --tail 40`
- Thinking 正常進行中は放置
- 出力末尾に以下が出たら対応:
  - `Allow execution of ...?` → `deckpilot send ghostty-33628 "2"` で session-wide 許可
  - `Shell awaiting input` → 子プロセスが debugger/REPL で stdin 待ち。
    `tasklist` で該当 process を特定し `taskkill /F /PID <pid>` で殺す。
    Go の `go test` や `go build` が詰まるなら `go.exe` 狙い。
  - 5 分超同じ Thinking → stall 疑い。`deckpilot send ghostty-33628 "状況を短く要約せよ"` で蹴る
  - `DISPATCH-DONE` → 監視終了

### 2. 絶対禁止
- `git push`、origin 反映、main 直コミット
- `0.0.0.0` bind を許す、認証を入れる、既存 CLI を壊す変更を通す
- `gh issue comment` / `gh pr create` を君自身が実行
- 33628 の worktree のファイル編集

### 3. 完了後のレポート
- 君の前回 worktree `../photo-ai-lisp-split` の `docs/atom-17.5-split-report.md`
  に追記**ではなく**、今回は deckpilot リポ内に新規ファイルで書く:
  `C:/Users/yuuji/deckpilot/docs/issue-27-ws-babysit-report.md`
- セクション:
  - ## 観測回数 / 介入回数 (承認送信数、taskkill 回数)
  - ## 33628 の最終判定 (DISPATCH-DONE に至ったか、コミット何本)
  - ## 成果物 (`git -C C:/Users/yuuji/deckpilot log --oneline main..feat/issue-27-ws-endpoint`)
  - ## ユーザーへの推奨次アクション (merge 可否、追加 PR 必要か)
- deckpilot リポ側で別 worktree を切る必要はない。33628 と同じリポで OK、
  ただし 33628 と**同時に feat/issue-27-ws-endpoint ブランチを触るな**。
  レポートは `main` 上に別ブランチ `docs/issue-27-babysit` を切って commit せよ。

### 4. 完了
`MANAGER-ROLE-2-DONE` を出力して停止。
