# ADR 0001 — orchestrator への通知は pull、push は採らない

- Status: Accepted
- Decided: 2026-04-30 (deckpilot 着工時)
- Re-confirmed: 2026-06-04 (一次情報で裏取り、下記 Sources)
- Corrected: 2026-06-05 (prior art `wt-sidecar` の見落としを訂正 ── 理由を「不可能」→「実用不可」に、決定は不変。Rationale 参照)

## Context

deckpilot のオーケストレータは Claude Code (claude) で、**turn-based**(ユーザー入力が来たターンの間だけ走り、合間は stdin を読む所でブロックして停止)。多くの場合 **stock の Windows Terminal** 上で動く。

worker(deck 経由で起動した子 claude)が「終わった / 途中で沈黙停止した」ことを、ユーザーが次に話す前に、**その瞬間にオーケストレータへ届けたい**(push)という欲求が繰り返し出る。

## Decision

worker → orchestrator の **event push は作らない**。代わりに UserPromptSubmit hook
(`~/.claude/hooks/orchestrator-status-pull.sh`)で、**ユーザーの各ターンに状態を pull-inject** する(deckpilot list / open issues / 直近 commit / 未読 worker progress)。

## Rationale(2026-06-04 起稿 / 2026-06-05 訂正)

**訂正の経緯**: 本 ADR は当初「stock WT への入力注入は技術的に不可能」と書いたが、それは誤り。本プロジェクトには既に prior art `~/wt-sidecar`(2026-04-08)が在り、stock WT への注入を**実際に実現していた**。その存在と「実用不可」という結論を、作業履歴DB を照会せず見落としていた(2026-06-05 に user 指摘で発見)。以下は訂正後の正しい理由 ── 決定(pull)は変わらず、理由を「不可能」から「実用不可」に差し替える。

stock WT で走る claude へ、外部プロセスから**非侵襲的に**入力を注入する supported path は無い:

- **ConPTY の入力はパイプ経由のみ**。入力は WT 所有の入力パイプに書かれ、他プロセスからは触れない。
- **WriteConsoleInput** は別プロセスから ConPTY 相手に access denied(MS も非推奨と明言)。
- **PostMessage(WM_CHAR)** は ConPTY が読まないため届かない。

唯一実現した注入経路が **wt-sidecar の方式** ── UIA で画面を読み、`SendInput` で入力を送る。だがこれは実用に耐えなかった:

- `SendInput` は宛先指定が無くフォアグラウンド固定。注入の度に `SetForegroundWindow` で対象を**前面に奪う**必要がある。
- = user が他作業中に勝手にフォーカスを奪って割り込む。「空恐ろしい / 迷惑」と user フィードバックで**中止**。CLAUDE.md「マウス・キー占有禁止」ルールの由来がこれ。

→ stock WT への push は技術的には可能だが、唯一の経路が侵襲的で実用不可。一方、**claude の入力パイプを握るホストの下で起動すれば**非侵襲に注入できる ── それが deckpilot 管理下 ghostty の制御パイプ(CP)で、`deckpilot send` が成立する理由。stock WT はその入力パイプを露出しないので、非侵襲な push は届かない。

ゆえに stock 端末のオーケストレータには pull(ユーザー発話で必ず読まれる)。瞬間 push が要るなら、オーケストレータ自身を管理下 ghostty で起動する(下記 Consequences)。

## Consequences

- オーケストレータが worker 停止を知るのは「**次のユーザー発話のターン**」(pull)。瞬間検知ではない。これは受容する。
- もし将来「瞬間検知」が要るなら、**オーケストレータ自身を stock WT ではなく deckpilot 管理下の ghostty で起動**し、CP 経由で `send` 注入する必要がある(別 ADR 候補)。
- worker 側の状態は watcher が status(active / idle / stalled)で持ち、pull に載る(ADR 候補: thinking-aware stall 判定)。

## Sources

- WriteConsoleInput function — Microsoft Learn: https://learn.microsoft.com/en-us/windows/console/writeconsoleinput
- AttachConsole() does not take user-input as expected — Microsoft Q&A: https://learn.microsoft.com/en-us/answers/questions/672400/
- Creating a Pseudoconsole session — Microsoft Learn: https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session
- Prior art(本プロジェクト内): `~/wt-sidecar`(2026-04-08, Gemini 2.0 Flash 製)── stock WT を UIA 読取 + `SendInput` 注入 + 名前付きパイプで CP に橋渡しし、deckpilot から ghostty 同様に扱えるようにした実装。注入の度に前面を奪う制約で実用不可と判明し、push の根拠ではなく「侵襲的経路は採らない」の根拠として残る。

## 出典・謝辞

このプロジェクトに ADR を導入する運用(`latest/` に現行版・`archive/NNNN/` に旧版を退避・番号は振り直さない、という鮮度管理)は、竹内一真氏(FIXER)の記事「Claude Code の Plan mode をやめてみる」(ascii.jp, 2026-06-04, https://ascii.jp/elem/000/004/407/4407056/ )に倣った。良い実践を公開して頂いたことに感謝する。なお ADR(Architecture Decision Records)という手法自体は Michael Nygard が提唱した一般的な実務であり、本 ADR の決定内容(push/pull)は当プロジェクト固有のもの。
