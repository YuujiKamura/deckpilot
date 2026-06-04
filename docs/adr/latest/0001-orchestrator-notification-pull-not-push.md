# ADR 0001 — orchestrator への通知は pull、push は採らない

- Status: Accepted
- Decided: 2026-04-30 (deckpilot 着工時)
- Re-confirmed: 2026-06-04 (一次情報で裏取り、下記 Sources)

## Context

deckpilot のオーケストレータは Claude Code (claude) で、**turn-based**(ユーザー入力が来たターンの間だけ走り、合間は stdin を読む所でブロックして停止)。多くの場合 **stock の Windows Terminal** 上で動く。

worker(deck 経由で起動した子 claude)が「終わった / 途中で沈黙停止した」ことを、ユーザーが次に話す前に、**その瞬間にオーケストレータへ届けたい**(push)という欲求が繰り返し出る。

## Decision

worker → orchestrator の **event push は作らない**。代わりに UserPromptSubmit hook
(`~/.claude/hooks/orchestrator-status-pull.sh`)で、**ユーザーの各ターンに状態を pull-inject** する(deckpilot list / open issues / 直近 commit / 未読 worker progress)。

## Rationale(2026-06-04 に一次情報で確認)

stock Windows Terminal で走る claude の stdin に、**外部プロセスから入力を注入する supported path は無い**:

- **ConPTY の入力はパイプ経由のみ**。疑似コンソールの入力は、ホスト(WT)が所有する入力パイプに書き込まれ、ConPTY が input record に変換して claude に渡す。そのパイプは WT 所有で、他プロセスからは触れない。
- **WriteConsoleInput** は別プロセスから ConPTY 相手に **access denied**。さらに MS は「非推奨、VT 等価物なし、他 OS に合わせ意図的に外していく」と明言。
- **SendInput** はフォアグラウンド固定(宛先引数なし)。
- **PostMessage(WM_CHAR)** は ConPTY がメッセージキュー経由の入力を読まないため届かない。

→ push が成立するのは「**claude の入力パイプを握るホストの下で起動した場合**」のみ。それがまさに deckpilot 管理下 ghostty の制御パイプ(CP)で、worker への `deckpilot send` が成立する理由。**stock WT はその入力パイプを露出しない**ので、stock WT のオーケストレータに push は届かない。

polling より「ユーザー発話で必ず読まれる」pull 経路の方が、stock 端末では物理的に確実。

## Consequences

- オーケストレータが worker 停止を知るのは「**次のユーザー発話のターン**」(pull)。瞬間検知ではない。これは受容する。
- もし将来「瞬間検知」が要るなら、**オーケストレータ自身を stock WT ではなく deckpilot 管理下の ghostty で起動**し、CP 経由で `send` 注入する必要がある(別 ADR 候補)。
- worker 側の状態は watcher が status(active / idle / stalled)で持ち、pull に載る(ADR 候補: thinking-aware stall 判定)。

## Sources

- WriteConsoleInput function — Microsoft Learn: https://learn.microsoft.com/en-us/windows/console/writeconsoleinput
- AttachConsole() does not take user-input as expected — Microsoft Q&A: https://learn.microsoft.com/en-us/answers/questions/672400/
- Creating a Pseudoconsole session — Microsoft Learn: https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session

## 出典・謝辞

このプロジェクトに ADR を導入する運用(`latest/` に現行版・`archive/NNNN/` に旧版を退避・番号は振り直さない、という鮮度管理)は、竹内一真氏(FIXER)の記事「Claude Code の Plan mode をやめてみる」(ascii.jp, 2026-06-04, https://ascii.jp/elem/000/004/407/4407056/ )に倣った。良い実践を公開して頂いたことに感謝する。なお ADR(Architecture Decision Records)という手法自体は Michael Nygard が提唱した一般的な実務であり、本 ADR の決定内容(push/pull)は当プロジェクト固有のもの。
