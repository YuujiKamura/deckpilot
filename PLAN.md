# auto-approvals agent-aware refactor

## Problem
Single flat pattern list (`approval_patterns.go`) causes false positives for Gemini.
Generic words "Waiting" and "Allow" appear in Gemini's normal output → Enter is sent repeatedly → Gemini TUI corrupted / hangs.

Root cause in `autoapprovals.go`: `sentEnter` is reset when the prompt *disappears*, so each time Enter causes the buffer to re-render (clearing then re-showing "Waiting"), a new Enter is fired. This creates an Enter loop.

## Solution

### 1. Agent-specific pattern sets

```
claude : "Action Required", "Enter to select", "Y/n", "Allow", "trust", "Waiting"
         (unchanged — existing behavior preserved)
gemini : "Y/n", "(y/N)"
         (only highly distinctive interactive prompts; excludes "Allow"/"Waiting")
codex  : "Would you like to run", "Press enter to confirm", "Yes, proceed",
         "Y/n", "Action Required"
         (covers codex 0.120+ interactive confirmation dialogs)
```

### 2. Agent detection

**Explicit (primary)**: `--agent <claude|gemini|codex>` flag added to `auto-approvals`.
Default: `"claude"` — preserves existing behaviour when flag is absent.

**Auto-detect (secondary)**: scan buffer text for agent-signature strings
(e.g., "Gemini" → gemini, "Codex" → codex). Applied only when `--agent` is
not specified. Falls back to "claude" when inconclusive.

### 3. False-positive self-recovery (backoff)

Track `entersSent` (enters sent since last backoff reset).
After each successful Enter send, increment. When `entersSent >= maxEntersBeforeBackoff` (3):
  - Log warning to stderr
  - Set `backoffUntil = now + 30s`
  - Reset `entersSent = 0`, `sentEnter = false`

While `now < backoffUntil`, skip all Enter sends (log in verbose mode).
After backoff period, the cycle restarts from zero.

This ensures at most 3 Enters every 30s regardless of false-positive loops.

### 4. Compatibility

- `deckpilot approve <session>` unchanged (alias wired in main.go stays)
- `DetectApprovalPrompt(content)` kept with same signature (calls `DetectApprovalPromptForAgent(content, "claude")`)
- New export: `DetectApprovalPromptForAgent(content, agent string)`
- `approvalPatterns` var retained as alias to `agentApprovalPatterns["claude"]`
  (existing test `TestApprovalPatterns_List` passes without modification)

## Files

| File | Change |
|------|--------|
| `cmd/approval_patterns.go` | Add `agentApprovalPatterns` map, `DetectApprovalPromptForAgent`; keep `approvalPatterns` alias and `DetectApprovalPrompt` |
| `cmd/autoapprovals.go` | Add `--agent` flag, buffer-based auto-detect, call `DetectApprovalPromptForAgent`, add backoff logic |
| `cmd/approval_patterns_test.go` | Add agent-specific tests; update `TestDetectApprovalPrompt_Patterns` for Waiting (now claude-only); existing tests remain valid |

## Unknowns

- Actual Gemini confirmation prompt strings in the wild: using only `"Y/n"` and `"(y/N)"` for now.
  Field test needed to confirm completeness.
- Buffer-based auto-detect heuristics are best-effort; `--agent` flag is the reliable path.
