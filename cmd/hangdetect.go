// Package cmd  E`deckpilot hang-detect` CLI entry point (issue #26).
//
// Wires a HangDetector to one session. Designed to be run alongside
// `deckpilot auto-approvals` or by itself when external monitoring is
// all that's needed.
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/YuujiKamura/deckpilot/daemon"
	"github.com/YuujiKamura/deckpilot/pipe"
)

var (
	hangDetectDaemonList = daemon.DaemonList
	hangDetectPipeTail   = pipe.Tail

	hangDetectDaemonListWithContext  = daemonListWithContext
	hangDetectPipeTailWithContext    = tailPipeWithContext
	hangDetectPipeHistoryWithContext = historyPipeWithContext
)

const hangDetectActionTimeout = 3 * time.Second

type hangDetectSessionEntry struct {
	Name     string `json:"name"`
	PID      int    `json:"pid"`
	PipePath string `json:"pipe_path"`
	WsURL    string `json:"ws_url,omitempty"`
	// Status mirrors the Watcher's current status string. The watcher
	// sets this to "stalled" when IsHungAppWindow(hwnd) is TRUE, which
	// probeOnce consults as its OS-authoritative fast path.
	Status     string `json:"status"`
	AppRuntime string `json:"app_runtime"`
	Uptime     string `json:"uptime"`
}

// HangDetect is the CLI command: deckpilot hang-detect <session> [flags]
//
//	--cpu-threshold N     percent (default 1.0)
//	--stall-seconds N     seconds of unchanged buffer (default 60)
//	--probe-interval DUR  gap between probes (default 5s)
//	--sample-interval DUR CPU sample window (default 500ms, must be < probe)
//	--on-hang ACTION      notify | snapshot | ctrl-c | tiered (default snapshot (recommended for evidence))
//	                      tiered = notify ↁEsnapshot ↁEctrl-c in sequence.
//	                      Destructive actions (kill) are intentionally
//	                      NOT exposed  Esee issue #26 addendum.
//	--include-children    default true; --no-include-children to disable
//	--once                run one probe and exit (diagnostic)
func HangDetect(args []string) {
	var sessionName string
	cpuThreshold := 1.0
	stallSeconds := 60 * time.Second
	probeInterval := 5 * time.Second
	sampleInterval := 500 * time.Millisecond
	onHang := "snapshot"
	includeChildren := true
	runOnce := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cpu-threshold":
			i++
			if i >= len(args) {
				hangUsageFatal("--cpu-threshold requires a value")
			}
			v, err := parseFloat(args[i])
			if err != nil {
				hangUsageFatal("invalid --cpu-threshold %q: %v", args[i], err)
			}
			cpuThreshold = v
		case "--stall-seconds":
			i++
			if i >= len(args) {
				hangUsageFatal("--stall-seconds requires a value")
			}
			v, err := parseFloat(args[i])
			if err != nil {
				hangUsageFatal("invalid --stall-seconds %q: %v", args[i], err)
			}
			stallSeconds = time.Duration(v * float64(time.Second))
		case "--probe-interval":
			i++
			if i >= len(args) {
				hangUsageFatal("--probe-interval requires a duration")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				hangUsageFatal("invalid --probe-interval %q: %v", args[i], err)
			}
			probeInterval = d
		case "--sample-interval":
			i++
			if i >= len(args) {
				hangUsageFatal("--sample-interval requires a duration")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				hangUsageFatal("invalid --sample-interval %q: %v", args[i], err)
			}
			sampleInterval = d
		case "--on-hang":
			i++
			if i >= len(args) {
				hangUsageFatal("--on-hang requires an action name")
			}
			onHang = args[i]
		case "--include-children":
			includeChildren = true
		case "--no-include-children":
			includeChildren = false
		case "--once":
			runOnce = true
		default:
			if strings.HasPrefix(args[i], "--") {
				hangUsageFatal("unknown flag %q", args[i])
			}
			if sessionName == "" {
				sessionName = args[i]
			}
		}
	}
	if sessionName == "" {
		hangUsageFatal("session name required")
	}

	// Resolve root PID from the daemon's session list.
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		os.Exit(1)
	}
	rootPID, err := resolveSessionPID(sessionName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hang-detect: %v\n", err)
		os.Exit(1)
	}

	// Action selection.
	action, err := pickHangAction(onHang, sessionName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hang-detect: %v\n", err)
		os.Exit(1)
	}

	cfg := daemon.HangConfig{
		SessionName:     sessionName,
		RootPID:         rootPID,
		Lister:          daemon.SystemProcessLister{},
		Sampler:         daemon.SystemCPUSampler{},
		LastActOf:       func() time.Time { return fetchLastAct(sessionName) },
		// Watcher already polls IsHungAppWindow and surfaces the
		// result as session status "stalled" via DaemonList. Plumb
		// that through so probeOnce can use the OS-authoritative
		// signal instead of guessing from CPU + last-act.
		WatcherStatusOf: func() string { return fetchSessionStatus(sessionName) },
		Action:          action,
		CPUThreshold:    cpuThreshold,
		StallSeconds:    stallSeconds,
		ProbeInterval:   probeInterval,
		SampleInterval:  sampleInterval,
		IncludeChildren: includeChildren,
	}
	det, err := daemon.NewHangDetector(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hang-detect: config: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr,
		"hang-detect: monitoring %s (pid=%d, cpu<%.1f%%, stall>%s, action=%s)\n",
		sessionName, rootPID, cpuThreshold, stallSeconds, onHang)

	if runOnce {
		// One probe and exit  Euseful for diagnostics / smoke tests.
		ctx, cancel := context.WithTimeout(context.Background(),
			probeInterval+sampleInterval+time.Second)
		defer cancel()
		_ = det.ProbeOnce(ctx)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\nstopped")
		cancel()
	}()

	if err := det.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "hang-detect: run: %v\n", err)
		os.Exit(1)
	}
}

func hangUsageFatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "hang-detect: "+format+"\n", a...)
	fmt.Fprintln(os.Stderr,
		"usage: deckpilot hang-detect <session> [--cpu-threshold N] [--stall-seconds N] "+
			"[--probe-interval DUR] [--sample-interval DUR] "+
			"[--on-hang notify|snapshot|ctrl-c|tiered] "+
			"[--include-children | --no-include-children] [--once]")
	os.Exit(1)
}

func parseFloat(s string) (float64, error) {
	// Avoid importing strconv twice by inlining; accepts plain decimals.
	var v float64
	_, err := fmt.Sscanf(s, "%f", &v)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// resolveSessionPID asks the daemon for sessions and returns the PID of
// the requested one. Returns error if the session is unknown or lacks
// a PID (web sessions).
func resolveSessionPID(sessionName string) (uint32, error) {
	all, err := listSessionsForHangDetect()
	if err != nil {
		return 0, err
	}
	for _, e := range all {
		if e.Name == sessionName {
			if e.PID <= 0 {
				return 0, fmt.Errorf("session %q has no PID", sessionName)
			}
			return uint32(e.PID), nil
		}
	}
	return 0, fmt.Errorf("session not found: %s", sessionName)
}

// resolveSessionPipePath asks the daemon for sessions and returns the named
// session's pipe path. Web sessions without a PTY pipe return an error.
func resolveSessionPipePath(sessionName string) (string, error) {
	all, err := listSessionsForHangDetect()
	if err != nil {
		return "", err
	}
	for _, e := range all {
		if e.Name == sessionName {
			if e.PipePath == "" {
				return "", fmt.Errorf("session %q has no pipe path", sessionName)
			}
			return e.PipePath, nil
		}
	}
	return "", fmt.Errorf("session not found: %s", sessionName)
}

func listSessionsForHangDetect() ([]hangDetectSessionEntry, error) {
	raw, err := hangDetectDaemonListWithContext(context.Background())
	if err != nil {
		return nil, err
	}
	var all []hangDetectSessionEntry
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return nil, fmt.Errorf("parse list: %w", err)
	}
	return all, nil
}

// fetchSessionStatus returns the named session's current status string
// from the daemon's session list ("idle", "active", "stalled",
// "dead", etc.). On any error, returns "" so the fast path falls back
// to the heuristic branch rather than spuriously firing. The watcher
// maintains "stalled" as its code word for IsHungAppWindow(hwnd)==TRUE.
func fetchSessionStatus(sessionName string) string {
	all, err := listSessionsForHangDetect()
	if err != nil {
		return ""
	}
	for _, e := range all {
		if e.Name == sessionName {
			return e.Status
		}
	}
	return ""
}

// fetchLastAct reads the session's last-act timestamp. When the daemon
// does not expose it explicitly (older protocol), we fall back to
// time.Now() so the stall clock starts fresh  Ethe CPU-idle check alone
// still prevents the detector from firing falsely on a busy process.
func fetchLastAct(sessionName string) time.Time {
	// The current SHOW protocol does not return last-act, so for now
	// we stamp Now() on every probe. Issue #26 M2 followup: extend
	// SHOW or add a dedicated LASTACT command. Harmless default: the
	// stall check is effectively disabled until the protocol is wired
	// up, leaving CPU-threshold as the sole hang criterion.
	return time.Now()
}

// pickHangAction returns a HangAction by name.
//
// Design (issue #26 addendum, 2026-04-19): the CLI never auto-kills a
// hung agent. Destructive termination loses the agent's reasoning
// chain and any queued input, which is strictly worse than the hung
// state the operator could have inspected manually. The only actions
// exposed here are strictly non-destructive:
//
//	notify    ELv1: log + supervisor ping
//	snapshot  ELv2: dump PTY tail to ~/.deckpilot/hang-dumps/
//	ctrl-c    ELv3: soft interrupt, agent may recover on its own
//	tiered    Eruns notify + snapshot + ctrl-c in sequence so the
//	           evidence is preserved before the interrupt might
//	           change screen state
//
// If an operator truly needs to terminate the process, `Stop-Process`
// is one shell invocation away  Eautomating that loses more than it
// gains.
func pickHangAction(name, sessionName string) (daemon.HangAction, error) {
	switch name {
	case "notify":
		return daemon.ActionNotify(nil), nil
	case "snapshot":
		return snapshotActionForSession(sessionName), nil
	case "ctrl-c":
		return daemon.ActionSendCtrlC(nil), nil
	case "tiered":
		return daemon.ChainActions(
			daemon.ActionNotify(nil),
			snapshotActionForSession(sessionName),
			daemon.ActionSendCtrlC(nil),
		), nil
	default:
		return nil, fmt.Errorf("unknown --on-hang action %q (valid: notify, snapshot, ctrl-c, tiered)", name)
	}
}

func snapshotActionForSession(sessionName string) daemon.HangAction {
	baseDir := hangDumpsDir()
	const tailLines = 200

	return func(ctx context.Context, info daemon.HangInfo) error {
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			return fmt.Errorf("ActionSnapshot: mkdir %s: %w", baseDir, err)
		}

		ts := info.DetectedAt
		if ts.IsZero() {
			ts = deckpilotNow()
		}
		filename := fmt.Sprintf("%s-%s.log",
			ts.Format("20060102-150405"),
			sanitizeFilename(info.SessionName))
		path := filepath.Join(baseDir, filename)

		// Pull the session's daemon-side metadata so the dump records
		// app_runtime / uptime / pipe_path. resolveSessionEntry is
		// best-effort: a missing entry must not block the dump (the
		// hang itself may have torn the session out of LIST).
		entry, entryErr := resolveSessionEntry(ctx, sessionName)

		// Tail first (most-recent context, easiest to read), then full
		// history below. Either may be empty if the pipe is unreachable.
		var tail, history string
		var tailErr, historyErr error
		if entryErr == nil && entry.PipePath != "" {
			tail, tailErr = hangDetectPipeTailWithContext(ctx, entry.PipePath, tailLines)
			history, historyErr = hangDetectPipeHistoryWithContext(ctx, entry.PipePath)
		}

		// Agent inference: prefer full history (more banner lines to
		// match against); fall back to tail. Keep the empty result as
		// "unknown" rather than silently defaulting to claude — this
		// dump is the resume hint, not an auto-approve decision.
		probeBuf := history
		if probeBuf == "" {
			probeBuf = tail
		}
		inferred := inferAgentFromBuffer(probeBuf)
		if inferred == "" {
			inferred = "unknown"
		}

		// Pull persisted launch metadata if `deckpilot launch` was the
		// one who started this session. Best-effort: missing file is
		// the common case for sessions opened manually, and a parse
		// error must not stop the dump.
		meta, metaOK, metaErr := ReadLaunchMeta(sessionName)

		var hb strings.Builder
		fmt.Fprintln(&hb, "# deckpilot hang snapshot v2")
		fmt.Fprintf(&hb, "# session: %s\n", info.SessionName)
		fmt.Fprintf(&hb, "# root_pid: %d\n", info.RootPID)
		fmt.Fprintf(&hb, "# process_count: %d\n", info.ProcessCount)
		fmt.Fprintf(&hb, "# cpu_percent: %.2f\n", info.CPUPercent)
		fmt.Fprintf(&hb, "# stall_since: %s\n", info.StallSince)
		fmt.Fprintf(&hb, "# detected_at: %s\n", ts.Format(time.RFC3339))
		fmt.Fprintf(&hb, "# inferred_agent: %s\n", inferred)
		if entryErr == nil {
			fmt.Fprintf(&hb, "# app_runtime: %s\n", entry.AppRuntime)
			fmt.Fprintf(&hb, "# uptime: %s\n", entry.Uptime)
			fmt.Fprintf(&hb, "# status: %s\n", entry.Status)
			fmt.Fprintf(&hb, "# pipe_path: %s\n", entry.PipePath)
			if entry.WsURL != "" {
				fmt.Fprintf(&hb, "# ws_url: %s\n", entry.WsURL)
			}
		} else {
			fmt.Fprintf(&hb, "# session_metadata_error: %v\n", entryErr)
		}
		switch {
		case metaErr != nil:
			fmt.Fprintf(&hb, "# launch_metadata_error: %v\n", metaErr)
			fmt.Fprintln(&hb, "# resume_hint: read the full history below to recover the in-flight task,")
			fmt.Fprintln(&hb, "#              then `deckpilot launch <inferred_agent> \"<continued prompt>\" --cwd <see buffer>`")
		case metaOK:
			fmt.Fprintf(&hb, "# launched_agent: %s\n", meta.Agent)
			fmt.Fprintf(&hb, "# launched_cwd: %s\n", meta.Cwd)
			fmt.Fprintf(&hb, "# launched_at: %s\n", meta.LaunchedAt.Format(time.RFC3339))
			fmt.Fprintf(&hb, "# original_prompt: %s\n", oneLineForHeader(meta.Prompt))
			fmt.Fprintf(&hb, "# resume_command: deckpilot launch %s %s --cwd %s\n",
				meta.Agent, shellQuote(meta.Prompt), shellQuote(meta.Cwd))
		default:
			fmt.Fprintln(&hb, "# launched_by: external (no launch-meta record — session was not started by `deckpilot launch`)")
			fmt.Fprintln(&hb, "# resume_hint: read the full history below to recover the in-flight task,")
			fmt.Fprintln(&hb, "#              then `deckpilot launch <inferred_agent> \"<continued prompt>\" --cwd <see buffer>`")
		}
		fmt.Fprintf(&hb, "# --- buffer tail (%d lines) ---\n", tailLines)

		var bb strings.Builder
		switch {
		case entryErr != nil:
			fmt.Fprintf(&bb, "[snapshot note: %v]\n", entryErr)
		case tailErr != nil:
			fmt.Fprintf(&bb, "[snapshot error: tail failed: %v]\n", tailErr)
		default:
			bb.WriteString(tail)
			if !strings.HasSuffix(tail, "\n") {
				bb.WriteString("\n")
			}
		}

		// History section: only emit when we got something. A
		// best-effort failure stays out of the dump — the tail above
		// is enough for triage, and an "[error]" line in the history
		// block would only confuse readers.
		if entryErr == nil {
			bb.WriteString("# --- full history (HISTORY|0) ---\n")
			switch {
			case historyErr != nil:
				fmt.Fprintf(&bb, "[history unavailable: %v]\n", historyErr)
			case history == "":
				bb.WriteString("[history empty]\n")
			default:
				bb.WriteString(history)
				if !strings.HasSuffix(history, "\n") {
					bb.WriteString("\n")
				}
			}
		}

		if err := os.WriteFile(path, []byte(hb.String()+bb.String()), 0o644); err != nil {
			return fmt.Errorf("ActionSnapshot: write %s: %w", path, err)
		}
		return nil
	}
}

// resolveSessionEntry returns the full LIST entry for the named
// session, including AppRuntime / Uptime / Status fields that snapshot
// v2 records as resume hints. Reuses the same context-bounded LIST
// path as resolveSessionPipePathWithContext.
func resolveSessionEntry(ctx context.Context, sessionName string) (hangDetectSessionEntry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, hangDetectActionTimeout)
	defer cancel()
	all, err := listSessionsForHangDetectWithContext(ctx)
	if err != nil {
		return hangDetectSessionEntry{}, err
	}
	for _, e := range all {
		if e.Name == sessionName {
			return e, nil
		}
	}
	return hangDetectSessionEntry{}, fmt.Errorf("session not found: %s", sessionName)
}

func listSessionsForHangDetectWithContext(ctx context.Context) ([]hangDetectSessionEntry, error) {
	raw, err := hangDetectDaemonListWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	var all []hangDetectSessionEntry
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return nil, fmt.Errorf("parse list: %w", err)
	}
	return all, nil
}

func daemonListWithContext(ctx context.Context) (string, error) {
	resp, err := dialDaemonWithContext(ctx, "LIST")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(resp, "ERR|") {
		return "", fmt.Errorf("%s", strings.TrimPrefix(resp, "ERR|"))
	}
	return strings.TrimPrefix(resp, "OK|"), nil
}

func tailPipeWithContext(ctx context.Context, pipePath string, lines int) (string, error) {
	resp, err := sendRecvPipeWithContext(ctx, pipePath, fmt.Sprintf("TAIL|%d", lines))
	if err != nil {
		return "", err
	}
	if errMsg, ok := pipe.IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	return pipe.StripTailHeader(resp), nil
}

// historyPipeWithContext fetches the full scrollback via HISTORY|0.
// Used by snapshot v2 so a hang dump preserves enough context for a
// human or another agent to reconstruct what the session was doing
// before it stopped responding.
func historyPipeWithContext(ctx context.Context, pipePath string) (string, error) {
	resp, err := sendRecvPipeWithContext(ctx, pipePath, "HISTORY|0")
	if err != nil {
		return "", err
	}
	if errMsg, ok := pipe.IsError(resp); ok {
		return "", fmt.Errorf("server: %s", errMsg)
	}
	return pipe.StripTailHeader(resp), nil
}

func dialDaemonWithContext(ctx context.Context, command string) (string, error) {
	return sendRecvPipeWithContext(ctx, daemon.IPCPipePath(), command)
}

func sendRecvPipeWithContext(ctx context.Context, pipePath, message string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, hangDetectActionTimeout)
	defer cancel()

	conn, err := winio.DialPipeContext(ctx, pipePath)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", pipePath, err)
	}
	defer conn.Close()

	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	if err := conn.SetDeadline(connectionDeadline(ctx)); err != nil {
		return "", fmt.Errorf("set deadline: %w", err)
	}
	if _, err := conn.Write([]byte(message)); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	tmp := make([]byte, 65536)
	n, err := conn.Read(tmp)
	if err != nil && n == 0 {
		return "", fmt.Errorf("read: %w", err)
	}
	resp := strings.TrimRight(string(tmp[:n]), "\r\n")
	if strings.HasPrefix(resp, pipe.PrefixTAIL) {
		var buf bytes.Buffer
		buf.WriteString(string(tmp[:n]))
		for {
			nn, err := conn.Read(tmp)
			if nn > 0 {
				buf.Write(tmp[:nn])
			}
			if err != nil {
				break
			}
		}
		return strings.TrimRight(buf.String(), "\r\n"), nil
	}
	return resp, nil
}

func connectionDeadline(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(hangDetectActionTimeout)
}

// NOTE: an interactive `recover` action that prompted via fmt.Scanln was
// removed in commit-after-26e3cff. hang-detect runs unattended (e.g.
// alongside auto-approvals), so a stdin prompt would block the monitor
// indefinitely. The advertised but unwired `recover`/`tiered-recover`
// values were dropped from the usage string and pickHangAction switch
// in the same change.
