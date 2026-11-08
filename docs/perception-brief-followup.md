# Perception follow-up

## Implemented

1. Ready failures preserve the last full buffer and readiness observations in `~/.deckpilot/launch-fail/`; launch windows remain open by default. The `--keep-on-fail` switch was not added because the existing failure path already retains the window. Closing a possibly misidentified session would be less safe during concurrent launches.
2. The failure record includes buffer sample/change counts, prompt-like-line observation, and last watcher status. Repeated command-prompt output is reported as an exited agent.
3. Dead list entries now show the watcher's last error and last three output lines. `deckpilot prune <session>` removes dead tracking only and never kills Ghostty.
4. Callback notifications are persisted to `~/.deckpilot/notifications.jsonl`; `deckpilot notify inbox` reads them without writing into an agent's terminal input.
5. `send` distinguishes accepted, queued, and no-effect outcomes from the initial watcher state and submit observation.
6. `wait-idle --tail N` includes recent terminal output. Idle remains explicitly distinct from task completion.
7. `version` already existed in source. Launch now checks each configured CLI and its launch flags with `--help`. The installed binary was not replaced because the running singleton daemon is older and does not implement the new raw-key IPC; replacing the CLI alone would leave trust handling incompatible until a safe daemon restart window.
8. The daemon recognizes a launched agent returning to a persistent Windows command prompt and marks it `dead` with an `agent exited` reason. It uses launch metadata so ordinary shell sessions are not misclassified.
9. `list` includes saved agent, cwd, and launch time in text and JSON output. It does not show a prompt or brief path: launch metadata stores prompt text, not a source path, and exposing it could disclose secrets. No path is synthesized from free-form prompt text.

## Skipped

10. `show --summary` was not added. Ghostty's retained terminal buffer is rendered UI text, not a structured tool-event stream: command echoes, test results, and model narration vary by agent and can be omitted or rewritten by terminal rendering. Summarizing those as recent tool runs would imply evidence the daemon does not have. A reliable implementation needs agent-native event hooks or structured output capture first.

## Reproduction note

The trust-dialog issue was reproduced from the supplied capture and its `No, exit` initial selection. The source-built CLI passed the Claude CLI preflight and created a test Ghostty session; the session was then closed by its own PID and the isolated daemon shut down. The `show` request timed out, so terminal readiness was not independently confirmed. Other Ghostty sessions were left untouched.
