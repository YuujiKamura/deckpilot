package daemon

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

// A Ghostty window can hold several tabs, each with its own shell or agent.
// The watcher reads only the active tab, so two things need the other tabs:
// the "agent exited" verdict (a shell prompt in the active tab means nothing
// while another tab still runs a worker) and `list`, which shows each tab.

// tabInfo is one tab of a window as `list` reports it.
type tabInfo struct {
	Index  int    `json:"index"`
	ID     string `json:"id"`
	Pwd    string `json:"pwd,omitempty"`
	Active bool   `json:"active"`
	// State: "shell" (back at a command prompt), "working" (an agent's
	// progress spinner is on screen), "open" (something else is running,
	// e.g. an agent waiting for input), "unknown" (the tab could not be read).
	State string `json:"state"`
}

// tabReader is the part of *pipe.Client that reads other tabs. Test fakes of
// pipeClient need not implement it; a watcher whose client does not is
// treated as a single-tab window.
type tabReader interface {
	ListTabs() (string, error)
	TailTab(lines int, tab string) (string, error)
}

// parseTabList reads a LIST_TABS reply into tabs (without State).
func parseTabList(raw string) []tabInfo {
	active := -1
	var tabs []tabInfo
	for _, ln := range strings.Split(raw, "\n") {
		f := strings.Split(strings.TrimSpace(ln), "|")
		switch {
		case f[0] == "LIST_TABS" && len(f) >= 3:
			if n, err := strconv.Atoi(f[2]); err == nil {
				active = n
			}
		case f[0] == "TAB" && len(f) >= 3:
			idx, err := strconv.Atoi(f[1])
			if err != nil {
				continue
			}
			t := tabInfo{Index: idx, ID: f[2]}
			for _, kv := range f[3:] {
				if v, ok := strings.CutPrefix(kv, "pwd="); ok {
					t.Pwd = v
				}
			}
			tabs = append(tabs, t)
		}
	}
	for i := range tabs {
		tabs[i].Active = tabs[i].Index == active
	}
	return tabs
}

// tabState classifies a tab's screen. A last line that is a Windows shell
// prompt means nothing is running in that tab.
func tabState(screen string) string {
	if lastLineIsShellPrompt(screen) {
		return "shell"
	}
	if LooksActivelyThinking(screen) {
		return "working"
	}
	return "open"
}

func lastLineIsShellPrompt(screen string) bool {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return windowsPromptLine.MatchString(line)
	}
	return false
}

// tabTailLines is how much of each tab is read to classify it.
const tabTailLines = 30

// readTabStates lists the window's tabs and classifies each one. A tab that
// cannot be read is "unknown".
func readTabStates(r tabReader) ([]tabInfo, error) {
	raw, err := r.ListTabs()
	if err != nil {
		return nil, err
	}
	tabs := parseTabList(raw)
	for i := range tabs {
		screen, err := r.TailTab(tabTailLines, strconv.Itoa(tabs[i].Index))
		if err != nil {
			tabs[i].State = "unknown"
			continue
		}
		tabs[i].State = tabState(screen)
	}
	return tabs, nil
}

// otherTabRunning reports whether a tab other than the active one is not
// at a shell prompt. Calling a window dead stops its watcher, so doubt keeps
// it alive: a BUSY tab list, or another tab that cannot be read, answers
// true. A tab list that fails otherwise (no LIST_TABS support, pipe gone) is
// a single-tab window as before tabs were considered: false.
func otherTabRunning(r tabReader) bool {
	raw, err := r.ListTabs()
	if err != nil {
		return pipe.IsBusy(err.Error())
	}
	tabs := parseTabList(raw)
	if len(tabs) < 2 {
		return false
	}
	for _, t := range tabs {
		if t.Active {
			continue
		}
		screen, err := r.TailTab(tabTailLines, strconv.Itoa(t.Index))
		if err != nil {
			return true
		}
		if !lastLineIsShellPrompt(screen) {
			return true
		}
	}
	return false
}

// oneShotTabs reads other tabs through fresh connections, with the BUSY
// retry of the pipe package's one-shot helpers. Used outside the watcher's
// own goroutine (list, revival checks).
type oneShotTabs struct{ path string }

func (o oneShotTabs) ListTabs() (string, error) { return pipe.ListTabs(o.path) }
func (o oneShotTabs) TailTab(lines int, tab string) (string, error) {
	return pipe.TailTab(o.path, lines, tab)
}

// tabCheckInterval bounds how often a watcher whose active tab sits at a
// shell prompt re-reads the other tabs (the check runs on every poll).
const tabCheckInterval = 5 * time.Second

// watcherTabCheck caches the last otherTabRunning answer per watcher.
type watcherTabCheck struct {
	mu      sync.Mutex
	at      time.Time
	running bool
}

// anotherTabRunning is otherTabRunning for this watcher's window, cached for
// tabCheckInterval. Called from the watcher's own goroutine only, which owns
// w.client.
func (w *Watcher) anotherTabRunning() bool {
	r, ok := w.client.(tabReader)
	if !ok {
		return false
	}
	w.tabCheck.mu.Lock()
	defer w.tabCheck.mu.Unlock()
	if !w.tabCheck.at.IsZero() && time.Since(w.tabCheck.at) < tabCheckInterval {
		return w.tabCheck.running
	}
	w.tabCheck.running = otherTabRunning(r)
	w.tabCheck.at = time.Now()
	return w.tabCheck.running
}

// agentExitedReason is the dead reason for a window whose launched agent
// returned to the shell prompt (and no other tab runs anything).
const agentExitedReason = "agent exited and returned to the command prompt"

// windowRunsSomethingAgain reports whether a window marked "agent exited"
// now has something running: its active tab left the shell prompt, or
// another tab is not at a prompt. Read errors answer false (the window stays
// as it is).
func windowRunsSomethingAgain(name, pipePath string) bool {
	screen, err := pipe.Tail(pipePath, tabTailLines)
	if err != nil {
		return false
	}
	if !isLaunchedAgentAtShellPrompt(name, screen) {
		return true
	}
	raw, err := pipe.ListTabs(pipePath)
	if err != nil || len(parseTabList(raw)) < 2 {
		return false
	}
	return otherTabRunning(oneShotTabs{path: pipePath})
}

// addTabs fills Tabs for every pipe session whose window has more than one
// tab. Windows are read in parallel so one slow window does not add up.
func addTabs(infos []sessionInfo) {
	var wg sync.WaitGroup
	for i := range infos {
		if infos[i].PipePath == "" || infos[i].WsURL != "" {
			continue
		}
		wg.Add(1)
		go func(s *sessionInfo) {
			defer wg.Done()
			tabs, err := readTabStates(oneShotTabs{path: s.PipePath})
			if err == nil && len(tabs) > 1 {
				s.Tabs = tabs
			}
		}(&infos[i])
	}
	wg.Wait()
}
