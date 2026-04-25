# [HUB-DISPATCH] regression issue 起票のみ (引き継ぎ)

## プロジェクト
C:/Users/yuuji/deckpilot

## 背景
前任 Gemini (ghostty-18612) が auto-approvals regression 調査を実施し、
以下を完了させた:
- ブランチ `docs/regression-investigation-2026-04-20` を作成
- commit `deeabe0 docs: auto-approvals regression investigation notes`
  (調査メモは既にリポ内にある)
- regression 再現確認済: 症状は以下の 2 行
  ```
  auto-approvals: monitoring ghostty-XXXXX (interval=2s, agent=auto, Ctrl+C to stop)
  auto-approvals: agent not detected, defaulting to claude
  ```

前任は issue 起票手前で shell stall を起こし kill された。君は引き継いで
**issue 起票だけ**を実施して停止する。

## 役割制約 (破るな)
1. **調査をやり直すな**。既に `deeabe0` の調査メモがある。内容を読め。
2. **コードを触るな**。docs/ 追加も禁止 (前任が既に書いた)。
3. **新規 auto-approvals を実行するな** (shell stall の再発を防ぐ)。
4. **他の .dispatch/*.md を読むな**。このファイル 1 本のみ。
5. `git push` 禁止、既存ブランチへの commit も不要。
6. 完了したら `DISPATCH-DONE` で停止。

## やること

### 1. 調査メモを読む
```
cd C:/Users/yuuji/deckpilot
git checkout docs/regression-investigation-2026-04-20
cat docs/auto-approvals-regression-investigation.md
```
このメモに書かれている内容を元に issue を書く。自分で再調査するな。

### 2. 関連 issue 確認 (軽く)
```
gh issue list --limit 10
```
既に同趣旨の issue が無いことだけ確認。あれば報告して停止。

### 3. issue 起票
`gh issue create` で以下の構造で起票:

- タイトル: `regression: auto-approvals exits immediately with "agent not detected" for Gemini sessions`
- 本文セクション:
  - ## Symptom (実出力 2 行)
  - ## Environment (Gemini CLI v0.38.2, Ghostty WinUI3, deckpilot 直近 commit hash)
  - ## Reproduction (静的コマンド例)
  - ## Last known green (2026-04-19, user 証言)
  - ## First known broken (2026-04-20)
  - ## Investigation notes (docs/auto-approvals-regression-investigation.md
    へのリンク + 要点 3〜5 行抜粋)
  - ## Important observation (5304 経由の auto-approvals では agent=gemini が
    正常検出された事例がある → regression は条件付き発火)
  - ## Workaround (`deckpilot send <sess> "2"` 手動)
  - ## Proposed next step (bisect 候補 commit range + 読むべき関数名、メモから抽出)

### 4. 完了
`DISPATCH-DONE` を出力して停止。
