---
name: deckpilot-model-router
description: deckpilot/Ghostty で複数キャリア（agy/codex/claude）のモデル群を「モデル格(tier)×思考の深さ(effort)」の2軸で外部カタログ＋スナップショット管理し、残枠制約と詰まり4分類（エフォート上げ・モデル格上げ・異種診断・損切り）で自動ルーティングする。デーモン無再起動でホットスワップ可能。Use when: オートマ, モデル自動切替, キャリア優先順, 週次枠, クォータ制限, deckpilot models, deckpilot launch, モデルスナップショット, エスカレーション, 異種モデル診断, エフォート
---

# deckpilot-model-router

複数キャリア（`agy` / `codex` / `claude`）のモデル群を **モデル格（`tier`: `light`/`standard`/`frontier`）× 思考の深さ（`effort`: `low`/`medium`/`high`/`xhigh`/`max`/`ultra`）** の2軸で管理し、`deckpilot` 単体（デーモン無再起動）でホットスワップ運用する。

## 1. コマンド早見表（すべてデーモン非依存）

```powershell
# 起動（動的解決＋agents.jsonフォールバック）
deckpilot launch auto-standard-medium "<タスク>" --cwd "<DIR>"
deckpilot launch auto-l2 "<タスク>" --cwd "<DIR>"          # L1=light:low / L2=standard:medium / L3=frontier:high
deckpilot launch codex-standard-medium "<タスク>"          # 異種診断・キャリア明示

# カタログ採取・履歴・ロールバック
deckpilot models snapshot [--force]
deckpilot models history
deckpilot models rollback <snapshot_filename>

# 状態確認・優先順・残枠制御・固定・同期
deckpilot models status [--json]
deckpilot models order codex agy claude                    # 別名: set-order
deckpilot models budget agy low --max-tier-low standard --max-effort-low medium --note "枠節約"
deckpilot models budget claude exhausted --reset-at 2026-12-01T00:00:00Z
deckpilot models budget claude ok                          # note/reset_at を自動クリア
deckpilot models pin agy standard gemini-4-neon            # auto で解除（カタログ不在は --force）
deckpilot models route --tier frontier --effort low        # 枯渇時は exit 2
deckpilot models sync                                      # agents.json を atomic 再同期

# 出力監視サーキットブレーカー（429はcooldown自動設定、上限枯渇は手動/指定reset_atまで停止）
deckpilot show <session> --tail 40 | deckpilot models check-output agy [--cooldown 15m] [--dry-run]
```

外部状態: `~/.deckpilot/model-catalog/current.json`・`snapshots/`・`model-policy.json`・`agents.json`（`DECKPILOT_HOME` / `--home` で切替可、`cleanup` 対象外）。

## 2. 初期2軸選定基準

| 用途・難易度 | 推奨プロファイル | 目安 |
| :--- | :--- | :--- |
| 単純調査・整形・単一ファイル軽修正 | `auto-light-low` (`auto-l1`) | 最速・最小コスト |
| 通常の実装・テスト作成・定型調査 | `auto-standard-medium` (`auto-l2`) | 既定の主力バランス |
| 複雑な実装・設計検討・難バグ解析 | `auto-standard-high` / `auto-frontier-medium` | まず同tierのeffort上げを優先 |
| 難問アーキテクチャ・最終判断 | `auto-frontier-high` (`auto-l3`) | 必要時のみ投入 |

## 3. 詰まり時の4分類とエスカレーション

前進が止まったら症状を4分類して `deckpilot models escalate` に掛ける（`STOP_AND_ASK_USER` は `exit 2`）：

```powershell
deckpilot models escalate --carrier agy --tier standard --effort low --kind capability --hops 1
deckpilot models escalate --carrier agy --tier standard --effort medium --kind loop --hops 1
```

| `--kind` | 症状の判断基準 | ルーターの動作 |
| :--- | :--- | :--- |
| `blocked` | 仕様矛盾・前提データ欠落・環境破損（不可能問題） | 即 **`STOP_AND_ASK_USER`**（上位モデルを浪費せず人間へ質問） |
| `quota` | 429 / レート制限 / 週次枠切れ | ホップ数を増やさず当該キャリアを `exhausted` 化し **`FAILOVER_CARRIER`** |
| `loop` | 同じエラー反復・思い込みループ | 同系統ギア上げを禁止し別キャリアへ **`SECOND_OPINION_OTHER_CARRIER`**（2系統不収束で停止） |
| `capability` | 方向性は妥当だが推論力不足 | ①同tierで **`EFFORT_UP`**（`light`は`medium`上限、他は`high`上限）→ ②次tierの`medium`へ **`TIER_UP`** → ③`frontier:high`不収束は別キャリア1回のみ試して停止 |

* `max_escalation_hops` 到達または後退ループ検知時は必ず **`STOP_AND_ASK_USER`**。

## 4. 安全弁

* 不可逆な削除・大規模上書き・外部送信/`git push`・仕様判断の分岐は自動実行せず人間に確認する。
