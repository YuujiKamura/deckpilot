// Package cmd — `deckpilot conduct` subcommand.
//
// Watches a markdown checklist file and dispatches a deckpilot launch for
// each unchecked `- [ ]` line, then rewrites that line to `- [x]` so the
// same task is not dispatched twice. Minimal substrate for the Symphony-
// style "tasks → autonomous coding agent" loop.
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"path/filepath"

	"github.com/YuujiKamura/deckpilot/daemon"
)

type ChecklistItem struct {
	LineIdx int  // 0-based line number in the source file
	Body    string
	Mark    string
}

var checkboxRe = regexp.MustCompile(`^\s*-\s+\[([ xX>!~])\]\s+(.+?)\s*$`)

// ParseChecklist parses markdown content and returns each `- [ ]` / `- [x]`
// line as a ChecklistItem. Non-matching lines are skipped.
func ParseChecklist(content string) []ChecklistItem {
	var out []ChecklistItem
	lines := strings.Split(content, "\n")
	inHTMLComment := false
	inCodeBlock := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Handle Code Block boundary
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}

		// Handle HTML Comment boundaries
		if strings.Contains(trimmed, "<!--") {
			inHTMLComment = true
		}

		isSkipped := inHTMLComment || inCodeBlock

		if strings.Contains(trimmed, "-->") {
			inHTMLComment = false
		}

		if isSkipped {
			continue
		}

		m := checkboxRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mark := m[1]
		out = append(out, ChecklistItem{
			LineIdx: i,
			Body:    strings.TrimSpace(m[2]),
			Mark:    mark,
		})
	}
	return out
}

func PickNextTodo(items []ChecklistItem) (ChecklistItem, bool) {
	for _, it := range items {
		if it.Mark == " " {
			return it, true
		}
	}
	return ChecklistItem{}, false
}

// MarkDispatched rewrites `- [ ]` to `- [x]` on the given 0-based line.
// Returns an error if the line is out of range, isn't a checkbox, or is
// already checked. Preserves the source's trailing-newline shape.
func MarkDispatched(content string, lineIdx int) (string, error) {
	hadTrailingNL := strings.HasSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	// strings.Split on "a\n" yields ["a",""], so the last empty entry is the
	// trailing-newline marker. Drop it for index sanity.
	if hadTrailingNL {
		lines = lines[:len(lines)-1]
	}
	if lineIdx < 0 || lineIdx >= len(lines) {
		return "", fmt.Errorf("MarkDispatched: line %d out of range (have %d lines)", lineIdx, len(lines))
	}
	line := lines[lineIdx]
	m := checkboxRe.FindStringSubmatchIndex(line)
	if m == nil {
		return "", fmt.Errorf("MarkDispatched: line %d is not a checkbox: %q", lineIdx, line)
	}
	// capture group 1 is the mark character. Its absolute indices in `line`
	// are m[2]..m[3].
	mark := line[m[2]:m[3]]
	if mark != " " {
		return "", fmt.Errorf("MarkDispatched: line %d already checked or running", lineIdx)
	}
	lines[lineIdx] = line[:m[2]] + ">" + line[m[3]:]
	out := strings.Join(lines, "\n")
	if hadTrailingNL {
		out += "\n"
	}
	return out, nil
}

type ConductArgs struct {
	File       string
	Agent      string
	Interval   time.Duration
	OneShot    bool // dispatch one and exit (smoke / dry-run friendly)
	MaxWorkers int
}

func ParseConductArgs(args []string) (ConductArgs, string) {
	out := ConductArgs{
		Agent:      "claude",
		Interval:   30 * time.Second,
		MaxWorkers: 3,
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file":
			if i+1 >= len(args) {
				return ConductArgs{}, "--file requires a path"
			}
			out.File = args[i+1]
			i++
		case "--agent":
			if i+1 >= len(args) {
				return ConductArgs{}, "--agent requires a value"
			}
			out.Agent = args[i+1]
			i++
		case "--interval":
			if i+1 >= len(args) {
				return ConductArgs{}, "--interval requires a duration (e.g. 30s, 1m)"
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return ConductArgs{}, fmt.Sprintf("--interval: %v", err)
			}
			out.Interval = d
			i++
		case "--max-workers":
			if i+1 >= len(args) {
				return ConductArgs{}, "--max-workers requires a number"
			}
			val, err := strconv.Atoi(args[i+1])
			if err != nil {
				return ConductArgs{}, fmt.Sprintf("--max-workers: %v", err)
			}
			out.MaxWorkers = val
			i++
		case "--one-shot":
			out.OneShot = true
		case "-h", "--help":
			return ConductArgs{}, "usage: deckpilot conduct --file PATH [--agent NAME] [--interval 30s] [--max-workers 3] [--one-shot]"
		default:
			return ConductArgs{}, fmt.Sprintf("unknown flag: %s", args[i])
		}
	}
	if out.File == "" {
		return ConductArgs{}, "--file is required"
	}
	return out, ""
}

// Conduct is the CLI entry point. Polls the checklist file at the given
// interval, dispatching one unchecked item per tick.
func Conduct(args []string) {
	parsed, errMsg := ParseConductArgs(args)
	if errMsg != "" {
		fmt.Fprintln(os.Stderr, errMsg)
		os.Exit(1)
	}
	if err := conductLoop(parsed, Launch); err != nil {
		fmt.Fprintf(os.Stderr, "conduct: %v\n", err)
		os.Exit(1)
	}
}

// launchFn is the seam for tests; the real implementation is cmd.Launch.
type launchFn func(args []string)

func conductLoop(cfg ConductArgs, launch launchFn) error {
	daemon.EnsureRunning()
	for {
		dispatched, err := conductTick(cfg, launch)
		if err != nil {
			return err
		}
		if cfg.OneShot {
			if !dispatched {
				fmt.Fprintln(os.Stderr, "conduct: no unchecked items, nothing to dispatch")
			}
			return nil
		}
		time.Sleep(cfg.Interval)
	}
}

func countActiveSessions() (int, error) {
	raw, err := daemon.DaemonList()
	if err != nil {
		return 0, err
	}
	var sessions []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		return 0, err
	}
	return len(sessions), nil
}

func conductTick(cfg ConductArgs, launch launchFn) (bool, error) {
	active, err := countActiveSessions()
	if err == nil && active >= cfg.MaxWorkers {
		fmt.Fprintf(os.Stderr, "conduct: max workers reached (%d/%d), waiting...\n", active, cfg.MaxWorkers)
		return false, nil
	}

	body, err := os.ReadFile(cfg.File)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", cfg.File, err)
	}
	items := ParseChecklist(string(body))
	todo, ok := PickNextTodo(items)
	if !ok {
		return false, nil
	}
	if isTaskAlreadyRunning(todo.Body) {
		fmt.Fprintf(os.Stderr, "conduct: task %q is already running in an active session, skipping duplicate launch\n", todo.Body)
		return false, nil
	}
	// Rewrite + flush BEFORE launching so a crash mid-launch cannot cause
	// the same task to be dispatched twice on next tick.
	newBody, err := MarkDispatched(string(body), todo.LineIdx)
	if err != nil {
		return false, fmt.Errorf("mark line %d: %w", todo.LineIdx, err)
	}
	if err := os.WriteFile(cfg.File, []byte(newBody), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", cfg.File, err)
	}
	fmt.Fprintf(os.Stderr, "conduct: dispatching %q -> agent=%s\n", todo.Body, cfg.Agent)
	
	metaPrompt := fmt.Sprintf("\n\n[SYSTEM ORCHESTRATION INSTRUCTION]\nあなたはシステムから投下された自律ワーカーです。タスクの全体計画は %s に記載されています。\nあなたのタスクは以下の行に相当します:\n`- [>] %s`\n作業が完了したら、必ず %s を編集して、上記の行の `[>]` を `[x]` に書き換えてください。もしエラーで完了できない場合は `[!]` に書き換えてください。その後終了してください。", cfg.File, todo.Body, cfg.File)
	
	launch([]string{cfg.Agent, todo.Body + metaPrompt})
	return true, nil
}

func isTaskAlreadyRunning(todoBody string) bool {
	raw, err := daemon.DaemonList()
	if err != nil {
		return false
	}
	var sessions []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		return false
	}

	homeDir := os.Getenv("USERPROFILE")
	if homeDir == "" {
		homeDir = os.Getenv("HOME")
	}

	for _, sess := range sessions {
		nameVal, ok := sess["name"]
		if !ok {
			continue
		}
		name, ok := nameVal.(string)
		if !ok {
			continue
		}

		metaPath := filepath.Join(homeDir, ".deckpilot", "launch-meta", name+".json")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}

		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta LaunchMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		if strings.HasPrefix(meta.Prompt, todoBody) {
			return true
		}
	}
	return false
}
