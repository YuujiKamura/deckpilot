# UIA Feasibility Assessment for Approval Modal Interaction (Issue #25)

## 1. UIA 基礎
Windows UI Automation (UIA) は、Windows 7 以降の標準アクセシビリティフレームワークです。
- **IUIAutomation**: UIA オブジェクトのエントリポイントであり、要素の検索やイベントのリスニングを制御します。
- **AutomationElement**: ウィンドウ、ボタン、テキストボックスなどの個々の UI コンポーネントを表します。
- **TreeWalker**: 指定された条件（例：ControlView, ContentView）に基づいて要素ツリーを走査し、親子・兄弟関係をナビゲートします。

## 2. Go からの接続方法
`github.com/mattn/go-ole` を使用して `UIAutomationCore.dll` にアタッチする手法が一般的かつ軽量です。

```go
import (
    "github.com/mattn/go-ole"
    "github.com/mattn/go-ole/oleutil"
)

func ConnectUIA() {
    ole.CoInitialize(0)
    defer ole.CoUninitialize()

    clsid, _ := ole.ClassIDFromProgramID("UIAutomationClient.CUIAutomation")
    unknown, _ := ole.CreateInstance(clsid, nil)
    uia, _ := unknown.QueryInterface(ole.IID_IDispatch)
    defer uia.Release()

    // ElementFromHandle や GetRootElement を呼び出してツリーを探索
}
```

## 3. Ghostty WinUI3 の子要素列挙 (2026-04-19 調査)
`ghostty.exe` (PID 28900) に対する PowerShell での UIA Tree 取得結果:

```text
Root Name: Ghostty [ghostty-28900]
Root ClassName: GhosttyWindow
Child elements count: 20
- Name: , ID: TabView, Class: Microsoft.UI.Xaml.Controls.TabView
- Name: ghostty-28900:t_001 ..., ID: , Class: ListViewItem
- Name: 閉じる, ID: CloseButton, Class: Button
- Name: 新しいタブの追加, ID: AddButton, Class: Button
- Name: GhosttyInputOverlay, ID: , Class: GhosttyInputOverlay
```

**考察**: WinUI3 XAML Islands で構築されているため、`AutomationId` が明示的に付与されている要素（TabView, CloseButton 等）は極めて安定して取得可能です。

## 4. 承認モーダルの可視性
エージェント毎の UIA 露出状況（推測含む）:

| Agent | UI Type | UIA Visibility | AutomationId / ClassName |
| :--- | :--- | :--- | :--- |
| **Claude Code** | Modal (XAML) | **Visible** | `ClassName: ContentDialog` |
| **Gemini 3** | Overlay (XAML) | **Visible** | `ClassName: GhosttyInputOverlay` |
| **Codex** | PTY Box | **Infeasible** | PTY 内部の描画（文字）であるため UIA からは見えない |

## 5. Invoke 可否
- **XAML 要素**: `ContentDialog` 内の [Approve] ボタン等は `InvokePattern` が公開されており、`Pattern.Invoke()` を呼ぶことでマウスクリックやキー入力を介さずに発火可能です。
- **PTY 要素**: 描画上の「ボタン」には UIA 要素が割り当たらないため、Invoke は不可能です。

## 6. PTY 非依存性の確認
- **利点**: XAML モーダルに対する UIA 操作は、PTY 入力バッファを一切汚染しません。
- **Layer 0 Mutex との関係**: UIA で操作が完結する場合、`handleSend` の Mutex ロックを待つ必要がなく、並行して走ることができます。これにより Layer 0/1 の根本的な脆弱性を回避できます。

## 7. 制約と未解決
- **PTY 内 UI の限界**: ターミナル画面内に描画される TUI モーダル（Codex等）は UIA では救えません。
- **非活性タブの取得**: Ghostty のタブが非活性（バックグラウンド）の場合、WinUI3 の仮想化により要素が Tree から消える可能性があるため、常時監視には向かない可能性があります。

## 結論
**Partial**

### 判定理由
Claude Code や Gemini のような **WinUI3 (XAML) 層で実装された承認プロンプト** に対しては、UIA は PTY を破壊しない完璧な回避策となり得ます。一方で、Codex のように PTY 内部で完結しているエージェントに対しては無力です。

### 最小 PoC 案
- PowerShell スクリプトにより `GhosttyInputOverlay` または `ContentDialog` を検出し、その配下の [Yes] 名前を持つ要素に `Invoke()` を発行する検証を先行実施すべき。
- 成功すれば、Layer 2 の `ApprovalAdapter` に `UiaAdapter` を追加し、XAML 層 UI を優先的に処理するハイブリッド構成が推奨されます。
