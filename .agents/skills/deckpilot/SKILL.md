---
name: deckpilot
description: deckpilotでGhostty/WinUI3上のAIセッションを操作する。(1) list/show/send/watch/launch の実行、(2) 自分のセッションと別セッションの識別、(3) Codex間の伝言・往復対話、(4) 相手エージェントに deckpilot 越しでこのセッションへ返答させる、(5) submit_unconfirmed 等の送信失敗切り分け。deckpilot、デックパイロット、watch、show、send、launch、セッション、Codex同士、対話、伝言、delegate、返答、relayと言われた時に使用。
---

# deckpilot

`deckpilot` を使って Ghostty/WinUI3 上の AI セッションを直接操作するときに使う。

## 基本手順

1. まず `deckpilot list` でセッション一覧を取る。
2. `deckpilot show <session>` で相手が誰か、今 idle か active かを確認する。
3. 送る時は `deckpilot send <session> "<message>"` を使う。
4. 承認待ち監視は `deckpilot watch <session>` を使う。
5. 新規起動が必要なら `deckpilot launch <agent> <prompt...>` を使う。

## Codex 起動時の注意

- `deckpilot launch codex ...` で起動しても、そのままでは毎回承認が出て自走しないことがある。
- Codex に継続作業をさせる前に、起動直後のセッションで approvals コマンドを打って承認モードを整える。
- 起動だけ成功しても送信や調査が止まる場合は、まず approvals 未設定を疑う。

## 自分のセッションを見分ける

- 指示を出す側は、まず「どれが自分のセッションか」を認識すること。
- 一番確実なのは `deckpilot show <candidate>` を見て、今この会話で直前に出した文面が表示されるセッションを自分と判断する方法。
- `deckpilot list` の `active` だけで決めない。別エージェントも active になりうる。
- 実運用では「今この会話をしている自分のセッションID」を最初に確定し、その ID を返答先として固定する。

## キャリア判定

- `deckpilot list` の `RUNTIME` 列を必ず見る。
- Ghostty 同士なら、相手自身に `deckpilot send <current-session>` を実行させる返却が成立しやすい。
- WT が混ざる時は、その前提が壊れることがある。WT 混在時は self-return を期待せず、`show` 回収を第一候補にする。
- つまり、返却戦略は「相手が誰か」だけでなく「Ghostty/WT のどちらか」を見て決める。

## 最重要ルール

- `ghostty-<pid>` のうち、今この会話をしている自分のセッションを別Codex扱いしない。必ず `show` の内容で相手確認をする。
- `list` に2件見えても、「別Codex」と「自分」の組み合わせであることがある。名前だけで判断しない。
- 「相手に調査をやらせる」ときは、結果回収を人間待ちにしない。相手自身に `deckpilot send <current-session> ...` を実行させて、このセッションへ返答させる。
- ただし WT 混在時は self-return 前提で組まない。WT がいるなら `show` でこちらが回収する fallback を最初から含める。

## 標準運用

### 1. こちらが中継する

1. 相手セッションへ `deckpilot send` で質問を送る。
2. `deckpilot show <session>` で返答を回収する。
3. 必要ならその返答を別セッションへ転送する。

これは最も単純で、送信経路の確認にも向く。

### 2. 相手に deckpilot 越しで返答させる

相手に仕事を投げるときは、返答方法まで含めて指示する。

例:

```text
この会話相手は ghostty-31256 です。調査が終わったら、あなた自身で deckpilot を使ってこのセッションへ結果を返してください。実行形式は:
deckpilot send ghostty-31256 "<要約>"
中間報告ではなく、結論が出てから返してください。
```

さらに厳格にするなら、送る本文まで固定する。

```text
この会話相手は ghostty-31256 です。調査完了後、あなた自身で次の形式で返答してください:
deckpilot send ghostty-31256 "結論: ... / 根拠: ..."
```

これを標準にする。`show` 回収は補助であって、第一経路ではない。

### 3. WT 混在時の fallback

WT が混ざるときは、最初から次の方針に切り替える。

```text
WT 混在の可能性があるので、deckpilot send での自己返却は前提にしない。調査結果は画面に要約表示しろ。こちらが deckpilot show で回収する。
```

これを明示しないと、「返すつもりで返せない」状態が起きる。

## 会話を成立させるコツ

- 相手に「1行だけ」「形式厳守」と明示しないと、質問文をそのまま繰り返すことがある。
- 形式を固定するなら `TO_B:` / `TO_A:` のように宛先付きにする。
- 調査依頼では「調査後に deckpilot send で返せ」を必ず含める。
- ただし WT 混在時は「deckpilot send で返せ」ではなく「show で回収するので画面に要約を出せ」と書く。

## 失敗時の見方

- `submit_ok|ok_cleared|...` は送信成功。
- `submit_unconfirmed|timeout_after_2000ms` は submit 確認失敗。相手側の trust 画面や UI 遷移が原因のことがある。
- `daemon did not start within 3s` は daemon 起動確認失敗。既存 daemon の状態を疑う。
- 相手が idle なのに返答が来ない場合は、`show <session>` で未送信の結論が残っていないか確認する。
- Codex 起動直後に毎回承認待ちになる場合は、approvals コマンド未実行の可能性が高い。
- WT 混在時に返答が来ないのは、相手の失敗ではなく self-return 不成立の可能性がある。その場合は `show` 回収へ切り替える。

## 実務上の判断

- 「単に相手に聞きたい」なら自分が中継する。
- 「相手が deckpilot を使って返せるか試したい」なら、相手自身に `deckpilot send` を実行させる。
- 「調査させる」なら、返答条件まで指示してから投げる。
- その前に、自分のセッションIDと相手の `RUNTIME` を確定する。
- Ghostty 同士なら self-return、WT 混在なら show 回収。この分岐を指示側が先に判断する。
- 「会話になっていない」と指摘されたら、まずプロトコル不足かセッション取り違えを疑う。
