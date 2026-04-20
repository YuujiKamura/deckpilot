# Ghostty WinUI3 apprt hang 仮説 (Issue #26 Track 2)

調査対象: Ghostty-win リポジトリの `src/apprt/winui3/` (YuujiKamura/ghostty, win ブランチ)
調査日: 2026-04-19
執筆: Hub (Claude Opus 4.7), Gemini quota 切れにより代打

---

## 1. アーキテクチャ概要 (EVIDENCE)

### 1.1 スレッドモデル

Ghostty WinUI3 は **2 つのスレッド** を跨いで動く:

- **UI スレッド**: XAML / DispatcherQueue / WM メッセージループ
- **CP パイプサーバースレッド**: deckpilot からの pipe 受信を処理

境界は厳密:
- `App.zig:1642` — `Do NOT call internal_os.open() on the UI/DispatcherQueue thread.`
- `control_plane.zig:523-525` — `Called from the pipe server thread. Read callbacks acquire renderer_state.mutex directly (no SendMessageW round-trip). Mutations use PostMessageW to the UI thread (async, no deadlock).`

### 1.2 deckpilot 入力が UI に届くまでの経路

1. deckpilot: `DaemonSend` → named pipe 書き込み
2. Ghostty: CP pipe thread が受信 → `provSendInput` 呼び出し
3. `provSendInput` が input を内部キューに `enqueueInput` (lock 付き)
4. `PostMessageW(hwnd, WM_APP_CONTROL_INPUT, ...)` で UI スレッドに非同期通知
5. UI スレッド: `WndProc` が `WM_APP_CONTROL_INPUT` を受け取り `cp.drainPendingInputs(&surface.core_surface)` で消化
6. 消化: surface core が PTY に書き込み、画面描画
7. 結果が PTY から戻って XAML (SwapChainPanel / IME TextBox) に反映

TSF 経路は別ルート: `\x1b[TSF:` prefix が先頭にある入力は `WM_APP_TSF_INJECT` 経由で IME 合成ライフサイクルを実行 (`App.zig:2462-2550` あたり)。

---

## 2. 仮説ごとの検証

### Q1. Thread affinity 違反があるか?

**仮説**: CP pipe thread が UI state を直接触っている箇所があり、race condition で hang する。

**証拠**: 現在のコードは明示的に `PostMessageW` で UI thread にマーシャリングしており、pipe thread から直接 XAML を触る箇所は確認できなかった。ただし `renderer_state.mutex` を pipe thread が直接取る ( `control_plane.zig:523` コメント) のは、**renderer が同じ mutex を UI thread で取りに行くとデッドロック** する設計。

**判定**: EVIDENCE あり、direct XAML 操作はしていないが mutex ordering は要注意。

**次検証**: `renderer_state.mutex` を両スレッドが取得する順序を全検索。

---

### Q2. Re-entrancy はあるか?

**仮説**: `WM_APP_CONTROL_INPUT` 処理中に別の CP 入力が到着し、再帰的に `WM_APP_CONTROL_INPUT` が発火、キュー競合で drain が stall する。

**証拠 (EVIDENCE)**: `control_plane.zig:551` の `PostMessageW` は **非同期**。UI thread が現行 `WM_APP_CONTROL_INPUT` を処理中でも、次の PostMessageW 呼び出しは queue に積まれる。ただし `WndProc` 内の処理は逐次 (Win32 メッセージループは単スレッド) なので、再入は起こらない構造。

ただし `surface.core_surface` の `writeKey` / `writeInput` 系が裏で **async タスクを起動** していた場合、次の WM_APP_CONTROL_INPUT が前回タスク完了前に発火し、2 並列の PTY 書き込みが混ざる可能性はある。

**判定**: SPECULATIVE (未確認部分あり)。

**次検証**: `core_surface.sendTextToPty` / `write` 系実装の同期/非同期を確認。

---

### Q3. Input queue 溢れで drop (**最有力**)

**仮説**: CP の queue が `max_pending_inputs` で制限されており、溢れたら **silently drop**。deckpilot の `send` (文字) + approve (Enter) の並列発火が queue を溢れさせ、hash 検証が通らず `text_not_visible` になる。

**証拠 (EVIDENCE)**:
- `control_plane.zig:547-549`:
  ```zig
  if (!self.enqueueInput("zig-cp", text, raw, cmd_id)) {
      log.warn("provSendInput dropped input due to full queue (max={})", .{self.max_pending_inputs});
      return cmd_id;
  }
  ```
- drop されても **cmd_id は返る** ので pipe クライアント (deckpilot) には "成功" 応答が返ってしまう
- deckpilot 側の post-submit hash 検証で "buffer が変わらない" → `submit_failed_stuck`

**2026-04-19 の観測との照合**:
- Codex 35572 の `text_not_visible|phase1_timeout` → queue drop が発生し typed text が画面に現れなかった
- Gemini 35984 の introspection loop → 承認 UI に関する input が drop された可能性
- Claude 未発症 → 承認 UI が無く keystroke 投入頻度が低いため queue 溢れなかった

**判定**: 証拠強度高。これが主犯と推定。

**deckpilot 側で打てる対策**:
1. ✅ (既に実装済) Layer 0 mutex: 1 セッションで同時 1 send に制限 → queue への投入レート抑制
2. ✅ (既に実装済) Phase 0.5 try-lock: approve がビジー時は skip → queue 過負荷を自動回避
3. 追加検討: `cmd_id` ベースの ACK 同期 (`provAckPoll` / `last_drained_cmd_id` は既に存在) を deckpilot が利用し、drain 完了後にだけ次を送る。今は async fire-and-forget。

---

### Q4. IME/TSF との衝突

**仮説**: deckpilot の通常テキスト入力が、ユーザーの IME composition 状態と衝突し TSF state machine を壊す。

**証拠 (EVIDENCE)**:
- `tsf.zig` 1458 行, `tsf_bindings.zig` 1707 行 は大規模 — composition lifecycle 管理
- `App.zig:2462-2550` の `WM_APP_TSF_INJECT` ハンドラは `OnStartComposition` → `OnUpdateComposition` → `textEditSinkOnEndEdit` → `tsfHandleOutput` のシーケンスを走らせる
- 通常 input 経路と TSF 経路は **同じ UI スレッドの WndProc で処理** される。composition 中に通常 input が来ると state conflict

**判定**: EVIDENCE あり、ただし deckpilot が TSF prefix を使わない限り通常経路だけなので、IME composition は ghostty 側 (Codex の承認 UI が TSF を使う場合) のみ発火。

**次検証**: Codex の承認 box 描画時に TSF が active になっているか実機確認。

---

### Q5. エージェント別挙動差の説明

**仮説**: Claude が発症しないのは承認 UI 無し = 入力頻度低 = queue 余裕あり、Codex/Gemini が発症するのは承認 UI による**複雑な state transition** が queue 消費を増やすため。

**証拠 (SPECULATIVE + EVIDENCE 混合)**:
- EVIDENCE: Claude は `--dangerously-skip-permissions` で承認モーダル無し、UI state transitions は最小
- SPECULATIVE: Codex/Gemini は承認時に内部再描画 (選択肢表示、焦点切替) を発生させ、**その間 UI thread が WM_APP_CONTROL_INPUT を drain できない** → queue 蓄積
- deckpilot が rapid-fire で送ると queue 溢れ → drop

**判定**: Q3 + この補強で agent 別挙動が説明できる。

---

## 3. 最も有力な単一仮説

**Q3 (queue 溢れで silent drop)** が主犯。

### 根拠
1. `text_not_visible` エラーと直接対応 — queue に入ってないので typed text が画面反映されない
2. agent 別挙動差を説明できる (承認 UI の再描画が UI thread drain を遅らせる)
3. `max_pending_inputs` による drop はコード上で明示されており silent
4. deckpilot 側の Layer 0/0.5 mutex を入れた後、まだ実機検証できていないが、**queue 投入レートを抑えれば改善するはず**

### deckpilot 側で完結する改善案
A. **既存**: Layer 0 mutex (Phase 0) + try-lock (Phase 0.5) で投入レートを 1 command/session に制限 → 実装済み
B. **提案**: `provAckPoll` を使って `last_drained_cmd_id` が追いつくまで次の send を待つ ACK 同期モード (新規プロトコル拡張)
C. **提案**: queue drop を検知する仕組み — Ghostty 側で drop があったら deckpilot に何らかのシグナルを返す (log だけだと deckpilot から見えない)

### Ghostty 側で要検討 (deckpilot の範疇外)
- `max_pending_inputs` の現在値を確認、適切かどうか評価
- drop 時に `provSendInput` が `cmd_id = 0` を返すなど、失敗を伝える API に変更
- UI thread drain の backpressure を CP server thread に伝える仕組み

---

## 4. 次の検証手段

1. **実機再現**: deckpilot に rapid-fire オプションを付けて 10 連 send → queue drop log が Ghostty 側に出るか確認
2. **ACK 同期実装**: `provAckPoll` を deckpilot から叩いて drain 完了待ちしてから次を送る PoC
3. **ログ相関**: Codex hang 時の Ghostty `log.warn("provSendInput dropped input...")` の有無を確認

---

## 5. まとめ

- deckpilot からの入力は **pipe thread → enqueue → PostMessageW → UI thread drain** の 2 段階経路
- **queue が溢れると silent drop**、これが `text_not_visible` の直接原因と推定
- Layer 0 (mutex) + Phase 0.5 (try-lock) は **queue 投入レートを抑える** 方向で有効
- 根本解は Ghostty apprt 側の backpressure API 整備 (upstream 要検討)
- deckpilot 単独でも ACK 同期モードを追加すれば当面の予防は可能
