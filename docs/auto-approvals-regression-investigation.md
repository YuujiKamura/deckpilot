# auto-approvals Gemini Detection Regression Investigation

Investigation into why `deckpilot auto-approvals` fails to detect Gemini agents and exits unexpectedly.

## 1. Symptoms
- `deckpilot auto-approvals <session>` prints `agent not detected, defaulting to claude`.
- It fails to auto-approve legitimate Gemini prompts (e.g., "Allow execution of...").
- In some cases, the command appears to exit immediately or shortly after starting.

## 2. Findings

### Detection Fragility (Regression 1)
The function `inferAgentFromBuffer` in `cmd/approval_patterns.go` only scans the last 30 lines of the session buffer. Banners (which contain "Gemini CLI") and prompt markers can easily be pushed out of this window by noise such as skill conflict warnings or tool outputs.

### Restricted Patterns (Regression 2)
In commit `7d55dfc`, Gemini-specific patterns were restricted to `{"Y/n", "(y/N)"}`. The pattern `"Allow"` was removed to prevent false positives from Gemini's status text ("Allow me to analyze..."). However, Gemini CLI's interactive execution prompt uses `"Allow execution of [...]?"`. Removing `"Allow"` completely broke auto-approval for these prompts.

### Permanent Fallback (Regression 3)
In `cmd/autoapprovals.go`, once detection fails and defaults to `claude`, it sets the `agentType` variable. Since the detection block is guarded by `if agentType == ""`, it never attempts detection again for the lifetime of the process. If the agent banner appeared *after* the first tick, it will never be detected as Gemini.

### Immediate Exit Mystery
While the code contains a `for { select { ... } }` loop that should not exit, observations in `ghostty-18612` show the command finishing. This might be due to a panic in `DetectApprovalPromptForAgent` or `inferAgentFromBuffer` that isn't being caught, or a deadlock in the `daemon` communication.

## 3. Reproduction
- Session: `ghostty-18612` (Gemini)
- Command: `deckpilot auto-approvals ghostty-5304 --interval 1s --dry-run`
- Result: 
  ```
  auto-approvals [dry-run]: monitoring ghostty-5304 (interval=1s, agent=auto, Ctrl+C to stop)
  auto-approvals: agent not detected, defaulting to claude
  ```
  (Command exited)

## 4. Proposed Fixes
1. **Increase detection window**: Scan last 100 lines instead of 30.
2. **Delayed/Retried detection**: If detection is inconclusive, don't set a permanent default immediately. Keep trying for the first 5-10 ticks.
3. **Refined Gemini patterns**: Add `"Allow execution of"` to `agentApprovalPatterns["gemini"]`. This is specific enough to avoid the status text false positives while catching the real prompts.
4. **Agent Persistence**: Allow the user to specify `--agent` as a reliable workaround.

