package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/daemon"
)

type workerStatus struct {
	Session        string        `json:"session"`
	State          string        `json:"state"`
	Cwd            string        `json:"cwd"`
	SinceLaunch    time.Duration `json:"-"`
	LaunchedMinAgo int           `json:"launched_min_ago"`
	ChangedFiles   int           `json:"changed_files"`         // from git status, excluding untracked files ending in ".local.md"
	LatestFile     string        `json:"latest_file,omitempty"` // changed file with the newest mtime, path relative to Cwd, forward slashes
	LatestAgeSec   int           `json:"latest_age_sec"`        // seconds since that mtime; -1 if no changed file
	Commits        int           `json:"commits_since_launch"`
	Children       []string      `json:"children,omitempty"`     // names of interesting child processes, e.g. "go.exe"
	ScreenWaits    string        `json:"screen_waits,omitempty"` // "menu", "trust", or ""
	Notes          []string      `json:"notes,omitempty"`        // things that could not be read, e.g. "cwd is not a git repository"
}

func parseGitStatusPorcelain(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		path = stripSurroundingQuotes(strings.TrimSpace(path))
		if path == "" || strings.HasSuffix(path, ".local.md") {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func stripSurroundingQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

var ignoredChildNames = []string{
	"conhost.exe",
	"cmd.exe",
	"powershell.exe",
	"pwsh.exe",
	"bash.exe",
	"sh.exe",
	"node.exe",
	"openconsole.exe",
	"ghostty.exe",
}

func trimExeSuffix(name string) string {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".exe") {
		return name[:len(name)-4]
	}
	return name
}

func interestingChildren(tree []daemon.ProcessInfo, agentExe string) []string {
	if len(tree) <= 1 {
		return []string{}
	}
	agentBase := trimExeSuffix(filepath.Base(strings.TrimSpace(agentExe)))
	seen := make(map[string]bool)
	var out []string
	for _, p := range tree[1:] {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		ignored := false
		for _, ig := range ignoredChildNames {
			if strings.EqualFold(name, ig) {
				ignored = true
				break
			}
		}
		if ignored {
			continue
		}
		if agentBase != "" && strings.EqualFold(trimExeSuffix(name), agentBase) {
			continue
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if out == nil {
		return []string{}
	}
	return out
}

func decideState(listStatus string, screenWaits string, children []string, latestAgeSec int) string {
	if listStatus == "dead" {
		return "dead"
	}
	if screenWaits != "" {
		return "waiting-for-answer"
	}
	if len(children) > 0 {
		return "running-command"
	}
	if latestAgeSec >= 0 && latestAgeSec < 120 {
		return "editing"
	}
	return "quiet"
}

func formatStatusLine(s workerStatus) string {
	parts := []string{s.Session, s.State}
	if s.ScreenWaits != "" {
		parts = append(parts, "screen: "+s.ScreenWaits)
	}
	if s.ChangedFiles == 0 {
		parts = append(parts, "files 0")
	} else {
		parts = append(parts, fmt.Sprintf("files %d (latest %s %ds ago)", s.ChangedFiles, s.LatestFile, s.LatestAgeSec))
	}
	parts = append(parts, fmt.Sprintf("commits %d", s.Commits))
	if len(s.Children) > 0 {
		parts = append(parts, "running "+strings.Join(s.Children, ","))
	}
	parts = append(parts, fmt.Sprintf("launched %dm ago", int(s.SinceLaunch.Minutes())))
	if len(s.Notes) > 0 {
		parts = append(parts, "note: "+strings.Join(s.Notes, "; "))
	}
	return strings.Join(parts, " | ")
}

// Status prints a single-line summary (or JSON) of what a worker is doing from its traces.
func Status(args []string) {
	session := ""
	jsonOut := false
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		} else if strings.HasPrefix(a, "-") {
			fmt.Fprintln(os.Stderr, "usage: deckpilot status <session> [--json]")
			os.Exit(2)
		} else if session == "" {
			session = a
		} else {
			fmt.Fprintln(os.Stderr, "usage: deckpilot status <session> [--json]")
			os.Exit(2)
		}
	}
	if session == "" {
		fmt.Fprintln(os.Stderr, "usage: deckpilot status <session> [--json]")
		os.Exit(2)
	}

	meta, ok, err := ReadLaunchMeta(session)
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "status: no launch record for %s (not started by deckpilot launch)\n", session)
		os.Exit(1)
	}

	now := deckpilotNow()
	sinceLaunch := time.Duration(0)
	if !meta.LaunchedAt.IsZero() {
		sinceLaunch = now.Sub(meta.LaunchedAt)
		if sinceLaunch < 0 {
			sinceLaunch = 0
		}
	}

	ws := workerStatus{
		Session:        session,
		Cwd:            meta.Cwd,
		SinceLaunch:    sinceLaunch,
		LaunchedMinAgo: int(sinceLaunch.Minutes()),
		LatestAgeSec:   -1,
	}

	listStatus := "dead"
	pid := 0
	listed := false
	if err := daemon.EnsureRunning(); err == nil {
		if entries, listErr := readSessionEntries(); listErr == nil {
			for _, e := range entries {
				if e.Name == session {
					listed = true
					listStatus = e.Status
					pid = e.PID
					break
				}
			}
		} else {
			ws.Notes = append(ws.Notes, fmt.Sprintf("session list: %v", listErr))
		}
	} else {
		ws.Notes = append(ws.Notes, fmt.Sprintf("daemon: %v", err))
	}

	if out, err := runGitWithTimeout(meta.Cwd, "status", "--porcelain"); err != nil {
		ws.Notes = append(ws.Notes, "git status failed")
	} else {
		paths := parseGitStatusPorcelain(out)
		ws.ChangedFiles = len(paths)
		var newestTime time.Time
		var newestPath string
		for _, p := range paths {
			fullPath := filepath.Join(meta.Cwd, filepath.FromSlash(p))
			info, statErr := os.Stat(fullPath)
			if statErr != nil {
				continue
			}
			if newestPath == "" || info.ModTime().After(newestTime) {
				newestTime = info.ModTime()
				newestPath = filepath.ToSlash(p)
			}
		}
		if newestPath != "" {
			ws.LatestFile = newestPath
			age := int(now.Sub(newestTime).Seconds())
			if age < 0 {
				age = 0
			}
			ws.LatestAgeSec = age
		}
	}

	sinceArg := "--since=" + meta.LaunchedAt.Format(time.RFC3339)
	if out, err := runGitWithTimeout(meta.Cwd, "rev-list", "--count", sinceArg, "HEAD"); err != nil {
		ws.Notes = append(ws.Notes, "git rev-list failed")
	} else {
		if count, parseErr := strconv.Atoi(strings.TrimSpace(out)); parseErr == nil {
			ws.Commits = count
		} else {
			ws.Notes = append(ws.Notes, "git rev-list parse failed")
		}
	}

	if listed && pid > 0 {
		tree, treeErr := daemon.GetProcessTree(daemon.SystemProcessLister{}, uint32(pid))
		if treeErr != nil {
			ws.Notes = append(ws.Notes, fmt.Sprintf("process tree: %v", treeErr))
		} else {
			agentExe := meta.Agent
			if def, resolveErr := resolveAgent(meta.Agent); resolveErr == nil && def.Cmd != "" {
				agentExe = def.Cmd
			}
			ws.Children = interestingChildren(tree, agentExe)
		}
	}

	screen, _, showErr := daemonShowFn(session, "buffer", getCaller())
	if showErr != nil {
		ws.Notes = append(ws.Notes, "screen unreadable")
	} else {
		if looksLikeTrustDialog(screen) {
			ws.ScreenWaits = "trust"
		} else if looksLikeMenu(screen) {
			ws.ScreenWaits = "menu"
		}
	}

	ws.State = decideState(listStatus, ws.ScreenWaits, ws.Children, ws.LatestAgeSec)

	if jsonOut {
		enc, err := json.MarshalIndent(ws, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "status: json marshal: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(enc))
		return
	}

	fmt.Println(formatStatusLine(ws))
}

func runGitWithTimeout(cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fullArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
