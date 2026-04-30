# Deckpilot Architectural and Code Review (2026-04-30)

This report provides a detailed review of the Deckpilot project, focusing on recent agent-specific detection changes, IPC stability, discovery scalability, and overall architectural health.

## 1. Executive Summary
Deckpilot has matured into a robust tool for orchestrating Ghostty sessions on Windows. The recent addition of agent-aware auto-approvals and parallelized discovery significantly improves user experience and performance. However, IPC stability remains a concern, specifically regarding Ghostty's input queue limits and potential pipe handle exhaustion ("Access is denied").

---

## 2. Agent-Specific Detection and Backoff (cmd/approval_patterns.go, cmd/autoapprovals.go)

### 2.1 Agent Detection Logic
*   **Findings**: The project successfully transitioned from a flat list of patterns to a structured map (`agentApprovalPatterns`).
*   **Pros**:
    *   **Granular Matching**: Different patterns for Claude, Gemini, and Codex prevent false positives in status output.
    *   **Smart Response**: The Gemini-specific response logic ("1" or "y") is a critical usability win.
*   **Cons**:
    *   **Detection Fragility**: `inferAgentFromBuffer` relies on string matching (e.g., "gemini>"). This may fail if agents update their prompts or if the buffer is noisy.
    *   **Fallbacks**: Defaulting to "claude" is a safe but potentially inaccurate fallback for newer agents.

### 2.2 Backoff Mechanism
*   **Findings**: Implements a 3-strike backoff (`maxEntersBeforeBackoff = 3`) with a 30s cooldown.
*   **Improvement**: The backoff state is correctly reset when the prompt disappears, allowing recovery once a manual or automated action clears the stall.

---

## 3. IPC Stability and Pipe Management

### 3.1 The "Access is denied" (ERROR_ACCESS_DENIED) Investigation
Logs indicate intermittent "Access is denied" errors during pipe communication.
*   **Potential Root Causes**:
    1.  **Handle Exhaustion**: `pipe.SendRecv` performs a full dial/write/read/close cycle for *every* message. On high-frequency operations (like polling), this can lead to handle leakage if any path skips `Close()`. Currently, `defer conn.Close()` is used, which is robust, but the sheer volume of handle creation/destruction is a stress point.
    2.  **Permission Conflicts**: If the daemon is started as Admin and the CLI as a standard user, `winio.DialPipe` will fail with "Access is denied".
*   **Ghostty Silent Drops**: Evidence from `docs/apprt-hang-hypothesis.md` shows Ghostty silent-drops inputs when its queue is full. Deckpilot's `SendRecv` doesn't currently check for this, leading to "stuck" states where the CLI thinks a command was sent but Ghostty never processed it.

### 3.2 Watcher Resilience
*   **Findings**: The `Watcher` goroutine is the "owner" of a session's health.
*   **Pros**: 
    *   **OS-Level Hang Detection**: `IsHungAppWindow` provides an independent health signal outside the IPC pipe.
    *   **Retry Logic**: 3 retries for pipe errors before marking a session dead prevents transient failures from killing the watcher.

---

## 4. Scalability of Ghostty Discovery (pipe/discovery.go)

### 4.1 Parallel vs. Serial
*   **Findings**: Discovery is now parallelized via `probePIDsConcurrent`.
*   **Performance**: Worst-case latency is now a single `Ping` budget (~2s) regardless of the number of ghostty instances. This is a massive improvement over the previous $O(N)$ serial approach which could stall for 30s+ with multiple hung windows.
*   **Grace Period**: The 10s grace period for session files is an effective workaround for the race condition between Ghostty creating the file and opening the pipe.

---

## 5. Architectural Debt and Potential Failure Modes

### 5.1 Lack of ACK Polling in Standard Commands
`pipe.SendWithSubmit` implements ACK polling, but `Watcher.handleRequest` (used by `deckpilot send`) uses a hardcoded `time.Sleep(100ms)`.
*   **Failure Mode**: If Ghostty is busy, 100ms is insufficient for the `INPUT` to clear the queue before the `\r` (Enter) is sent. This causes "mis-fires" where Enter is applied to the wrong state.

### 5.2 Watcher Resource Cleanup
While dead goroutines stop, the `Daemon`'s `watchers` map never removes entries for truly gone processes.
*   **Debt**: Long-running daemons will accumulate "dead" session entries.

### 5.3 Mutex Scope
The `Daemon` uses a single `mu sync.Mutex`.
*   **Failure Mode**: High-frequency IPC requests combined with periodic `refreshSessions` (which performs disk I/O and pipe pings) could lead to lock contention, making the daemon unresponsive.

---

## 6. Recommendations

1.  **Implement Connection Pooling**: Refactor `pipe.SendRecv` to optionally reuse established pipe connections within the `Watcher` goroutine to reduce handle churn and "Access is denied" risks.
2.  **Unify ACK Polling**: Update `Watcher.handleRequest` to use `pipe.AckPoll` for all `sendkeys` operations, ensuring the Ghostty queue has drained before proceeding.
3.  **Robust Agent Fingerprinting**: Consider adding metadata to Ghostty's `.session` files (like an `AGENT_TYPE` env var) so Deckpilot doesn't have to guess based on buffer strings.
4.  **Prune Dead Sessions**: Update `refreshSessions` to remove session entries from the map if the PID is no longer alive (`isProcessAlive(pid) == false`) for more than 1 minute.
5.  **Audit Permissions**: Add a warning to the CLI if the daemon's process owner differs from the current user, as this is the primary cause of named pipe "Access is denied" errors in Windows.
