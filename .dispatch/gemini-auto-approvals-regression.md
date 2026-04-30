# [HUB-DISPATCH] auto-approvals regression 原因ヒント探し

## プロジェクト
C:/Users/yuuji/deckpilot (PUBLIC)

## 背景
- 2026-04-19 (昨日): `deckpilot auto-approvals <session> --interval 2s` が
  Gemini セッション相手に正常に monitoring loop に入っていた
- 2026-04-20 (今日): 同じコマンドが以下の 2 行を吐いて exit code 0 で即終了
  ```
  auto-approvals: monitoring ghostty-XXXXX (interval=2s, agent=auto, Ctrl+C to stop)
  auto-approvals: agent not detected, defaulting to claude
  ```
  monitoring loop に入らない。
- ユーザー証言: 昨日〜今日の間に CP 耐性強化の改修を入れた。ハングは
  消えたがこれが副作用として agent 検出を壊したと推定。
- **方針**: revert しない。fix しない。**ヒント探し** のみ。

## ゴール
deckpilot リポ内で、auto-approvals の Gemini agent 検出が壊れた原因を
**コード読みとコミット履歴読み** で推定し、修正の最短経路を issue として
起票する。bisect 実行は任意 (読みで足りるなら省略)。

## やること

### 1. 再現確認 (任意、daemon を触らずに静的に読めるなら省略)
- 既に 4 つの session (ghostty-37016 自身, 5304, 33628, 39704) が稼働中。
  これらの daemon に新規 auto-approvals を発行して再現するのは安全だが、
  実行中の session を壊さないよう `--dry-run` があればそれを使う。
- 再現不要なら静的コード調査に進む。

### 2. 変更履歴の洗い出し
```
cd C:/Users/yuuji/deckpilot
git log --since="2026-04-18" --until="2026-04-20 23:59" --oneline
git log --since="2026-04-18" --until="2026-04-20 23:59" --stat -- '*auto*' '*approve*' '*agent*'
```
- auto-approvals 関連ファイル (cmd/, daemon/, pkg/) の最近の変更を列挙
- 特に「agent detection」「CP 耐性」「hang detect」「PTY」「yolo」
  あたりのキーワードを含むコミットをマークアップ

### 3. agent detection ロジックの特定
- `agent not detected, defaulting to claude` の文字列を grep で検索
- 周辺の関数を読み、どの信号で agent 種別を判定しているか特定
  (プロセス名 / バッファ内キーワード / PTY buffer hash 等)
- 昨日まで動いていた判定経路が壊れているなら:
  - 判定対象のバッファが変わった (CP 耐性改修で buffer format 変更?)
  - Gemini CLI の welcome 画面が変わった (バージョンアップ)
  - timing 問題 (phase1 timeout が早すぎる)
  のいずれに該当しそうかを推定

### 4. 関連 issue / PR の relevance 確認
- Issue #23 `refactor: pure buffer-hash submit detection` が buffer-hash
  系の refactor。agent detection が buffer-hash に依存しているなら、
  これの実装の過程で regression した可能性あり
- Issue #25 `auto-approve が PTY を破壊する` が直接関連
- 該当 PR / commit を読み、agent detection 側のテストがあるかも確認

### 5. 成果物: regression issue の起票
`gh issue create` で新規 issue を立てる (許可あり):

- タイトル: `regression: auto-approvals exits immediately with "agent not detected" for Gemini sessions`
- 本文に含めるもの:
  - ## Symptom (実際の出力 2 行)
  - ## Environment (deckpilot version/commit, Gemini CLI v0.38.2, Ghostty WinUI3)
  - ## Reproduction (静的コマンド例)
  - ## Last known green (commit / 日付)
  - ## First known broken (今日)
  - ## Suspected cause (コード読みで特定した関数名 + コミット hash)
  - ## Proposed investigation (次の一歩、修正方針のヒント。revert は選択肢に入れない)
  - ## Workaround (`deckpilot send <sess> \"2\"` を手動で送る現行運用)

### 6. ローカル調査メモ
`C:/Users/yuuji/deckpilot/docs/auto-approvals-regression-investigation.md`
に調査の生メモを保存 (grep 結果、読んだ関数名、仮説一覧)。
これは issue 本文には貼らず、ローカル参照用。

### 7. コミット
- ブランチ: `docs/regression-investigation-2026-04-20` を main から切る
- commit: `docs: auto-approvals regression investigation notes`
  (docs/ 配下のみ)

## 制約 (重要)
1. **revert 禁止** — 壊れたコミットを戻すな
2. **fix 禁止** — コードの修正は出すな。調査とレポートのみ
3. `git push` 禁止 (commit は OK)
4. `main` への直接 commit 禁止
5. 実行中の deckpilot daemon の再起動 / kill 禁止
6. `gh issue create` は regression 用 1 本に限り許可
7. 他の issue / PR へのコメント禁止
8. scope creep 禁止 (hang detector #26, PTY 破壊 #25 の再設計には踏み込むな)
9. 完了したら `DISPATCH-DONE` を出力して停止

## 期待される所要時間
30〜90 分。bisect せず読みで結論しない場合は「読んだ範囲 + 次の bisect
候補コミット range」を issue に書いて終わらせる。
