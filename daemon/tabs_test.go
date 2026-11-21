package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// Screens a tab can show, by state.
var tabScreens = map[string]string{
	"shell":   "Microsoft Windows\n(c) Microsoft Corporation.\n\nC:\\Users\\me>\n\n\n",
	"working": "● Bash(go test ./...)\n\n· Considering… (12s · ↓ 300 tokens)\n\n> \n",
	"open":    "Done. Anything else?\n\n───────\n> \n───────\n? for shortcuts\n",
}

func TestParseTabList(t *testing.T) {
	got := parseTabList("LIST_TABS|3|2\r\nTAB|0|t_000||pwd=C:\\a|prompt=0\nTAB|1|t_001||prompt=1\nTAB|2|t_005||pwd=C:\\c\nTAB|x|bad\nnoise\n")
	want := []tabInfo{
		{Index: 0, ID: "t_000", Pwd: "C:\\a"},
		{Index: 1, ID: "t_001"},
		{Index: 2, ID: "t_005", Pwd: "C:\\c", Active: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tab %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(parseTabList("")) != 0 || len(parseTabList("LIST_TABS|0|0\n")) != 0 {
		t.Fatal("empty list must give no tabs")
	}
}

func TestTabState(t *testing.T) {
	for want, screen := range tabScreens {
		if got := tabState(screen); got != want {
			t.Errorf("tabState(%s screen) = %q", want, got)
		}
	}
	if got := tabState(""); got != "open" {
		t.Errorf("empty screen = %q, want open", got)
	}
	if got := tabState("D:\\work\\deep path>   "); got != "shell" {
		t.Errorf("prompt with spaces = %q, want shell", got)
	}
}

// fakeTabs is a tabReader over a fixed set of tab screens. A screen of
// "<err>" fails to read; listErr fails the tab list.
type fakeTabs struct {
	screens []string
	active  int
	listErr error
	lists   atomic.Int64
	tails   atomic.Int64
}

func (f *fakeTabs) ListTabs() (string, error) {
	f.lists.Add(1)
	if f.listErr != nil {
		return "", f.listErr
	}
	var b strings.Builder
	fmt.Fprintf(&b, "LIST_TABS|%d|%d\n", len(f.screens), f.active)
	for i := range f.screens {
		fmt.Fprintf(&b, "TAB|%d|t_%03d||pwd=C:\\tab%d\n", i, i, i)
	}
	return b.String(), nil
}

func (f *fakeTabs) TailTab(lines int, tab string) (string, error) {
	f.tails.Add(1)
	i, err := strconv.Atoi(tab)
	if err != nil || i < 0 || i >= len(f.screens) {
		return "", errors.New("no such tab")
	}
	if f.screens[i] == "<err>" {
		return "", errors.New("read failed")
	}
	return f.screens[i], nil
}

// Every window shape: tab count 1..3 x active tab x each other tab in
// {shell, working, open, unreadable}, plus tab-list failures.
func TestOtherTabRunningCartesian(t *testing.T) {
	kinds := []string{"shell", "working", "open", "<err>"}
	n := 0
	for count := 1; count <= 3; count++ {
		for active := 0; active < count; active++ {
			// assignments of kinds to the non-active tabs
			others := count - 1
			total := 1
			for i := 0; i < others; i++ {
				total *= len(kinds)
			}
			for combo := 0; combo < total; combo++ {
				screens := make([]string, count)
				wantRunning := false
				c := combo
				for i := 0; i < count; i++ {
					if i == active {
						screens[i] = tabScreens["shell"]
						continue
					}
					k := kinds[c%len(kinds)]
					c /= len(kinds)
					if k == "<err>" {
						screens[i] = "<err>"
					} else {
						screens[i] = tabScreens[k]
					}
					if k != "shell" {
						wantRunning = true
					}
				}
				n++
				f := &fakeTabs{screens: screens, active: active}
				if got := otherTabRunning(f); got != wantRunning {
					t.Errorf("count=%d active=%d combo=%d: got %v, want %v", count, active, combo, got, wantRunning)
				}
				if f.tails.Load() > int64(count-1) {
					t.Errorf("read %d tabs, active tab must not be read", f.tails.Load())
				}
			}
		}
	}
	// 1 + 2*4 + 3*16
	if n != 1+2*4+3*16 {
		t.Fatalf("generated %d shapes", n)
	}

	for _, c := range []struct {
		err  error
		want bool
	}{
		{errors.New("server: BUSY|renderer_locked"), true},
		{errors.New("server: BUSY|data_lane_full"), true},
		{errors.New("dial: The system cannot find the file specified."), false},
		{errors.New("server: UNKNOWN_COMMAND"), false},
	} {
		if got := otherTabRunning(&fakeTabs{listErr: c.err}); got != c.want {
			t.Errorf("list error %q: got %v, want %v", c.err, got, c.want)
		}
	}
}

func TestReadTabStates(t *testing.T) {
	f := &fakeTabs{screens: []string{tabScreens["open"], "<err>", tabScreens["shell"], tabScreens["working"]}, active: 2}
	tabs, err := readTabStates(f)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"open", "unknown", "shell", "working"}
	for i, s := range want {
		if tabs[i].State != s || tabs[i].Active != (i == 2) || tabs[i].ID != fmt.Sprintf("t_%03d", i) {
			t.Errorf("tab %d = %+v, want state %s", i, tabs[i], s)
		}
	}
	if _, err := readTabStates(&fakeTabs{listErr: errors.New("x")}); err == nil {
		t.Error("list failure must be an error")
	}
}

// tabClient is a watcher pipeClient that also reads other tabs.
type tabClient struct {
	fakeClient
	*fakeTabs
}

func writeLaunchMeta(t *testing.T, home, session string) {
	t.Helper()
	p := filepath.Join(home, ".deckpilot", "launch-meta", session+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"session_name": session, "agent": "agy", "agent_started": true})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The "agent exited" verdict across: launched or not x active tab at a
// prompt or not x client that reads tabs or not x other tab state.
func TestWatcherExitVerdictConsidersOtherTabs(t *testing.T) {
	home := t.TempDir()
	prev := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = prev })
	writeLaunchMeta(t, home, "launched")

	for _, launched := range []bool{true, false} {
		for _, activeState := range []string{"shell", "working", "open"} {
			for _, readsTabs := range []bool{true, false} {
				for _, other := range []string{"none", "shell", "working", "open", "<err>"} {
					name := "external"
					if launched {
						name = "launched"
					}
					screens := []string{tabScreens[activeState]}
					if other != "none" {
						if other == "<err>" {
							screens = append(screens, "<err>")
						} else {
							screens = append(screens, tabScreens[other])
						}
					}
					var client pipeClient = &fakeClient{}
					if readsTabs {
						client = &tabClient{fakeTabs: &fakeTabs{screens: screens, active: 0}}
					}
					w := newWatcherForTest(name, client)
					w.updateContent(tabScreens[activeState])

					otherRuns := readsTabs && other != "none" && other != "shell"
					wantDead := launched && activeState == "shell" && !otherRuns
					if got := w.Status() == "dead"; got != wantDead {
						t.Errorf("launched=%v active=%s readsTabs=%v other=%s: dead=%v, want %v",
							launched, activeState, readsTabs, other, got, wantDead)
					}
					if wantDead && w.Profile().LastError != agentExitedReason {
						t.Errorf("dead reason %q", w.Profile().LastError)
					}
				}
			}
		}
	}
}

// The other-tab check runs only when the active tab is at a prompt, and at
// most once per tabCheckInterval.
func TestWatcherTabCheckIsCachedAndLazy(t *testing.T) {
	home := t.TempDir()
	prev := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = prev })
	writeLaunchMeta(t, home, "launched")

	ft := &fakeTabs{screens: []string{tabScreens["shell"], tabScreens["working"]}, active: 0}
	w := newWatcherForTest("launched", &tabClient{fakeTabs: ft})
	for i := 0; i < 5; i++ {
		w.updateContent(tabScreens["open"])
	}
	if ft.lists.Load() != 0 {
		t.Fatalf("tabs read %d times while the active tab was not at a prompt", ft.lists.Load())
	}
	for i := 0; i < 10; i++ {
		w.updateContent(tabScreens["shell"])
	}
	if ft.lists.Load() != 1 {
		t.Fatalf("tabs read %d times in one interval, want 1", ft.lists.Load())
	}
	if w.Status() == "dead" {
		t.Fatal("window with a working tab marked dead")
	}
	w.tabCheck.at = time.Now().Add(-tabCheckInterval - time.Second)
	ft.screens[1] = tabScreens["shell"]
	w.updateContent(tabScreens["shell"] + " ")
	if ft.lists.Load() != 2 {
		t.Fatalf("expired check not refreshed (%d reads)", ft.lists.Load())
	}
	if w.Status() != "dead" {
		t.Fatalf("all tabs at a prompt: status %q, want dead", w.Status())
	}
}

// fakeWindow is a control-plane pipe serving a window of tabs (LIST_TABS,
// TAIL with and without a tab), each tab showing screens[i].
type fakeWindow struct {
	path    string
	mu      sync.Mutex
	screens []string
	active  int
}

var fakeWindowSeq atomic.Int64

func newFakeWindow(t *testing.T, active int, screens ...string) *fakeWindow {
	t.Helper()
	f := &fakeWindow{
		path:    fmt.Sprintf(`\\.\pipe\deckpilot-test-window-%d-%d`, time.Now().UnixNano(), fakeWindowSeq.Add(1)),
		screens: screens,
		active:  active,
	}
	l, err := winio.ListenPipe(f.path, nil)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req, _ := bufio.NewReader(c).ReadString('\n')
				c.Write([]byte(f.reply(strings.TrimSpace(req))))
			}()
		}
	}()
	return f
}

func (f *fakeWindow) reply(req string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.Split(req, "|")
	switch p[0] {
	case "LIST_TABS":
		var b strings.Builder
		fmt.Fprintf(&b, "LIST_TABS|%d|%d\n", len(f.screens), f.active)
		for i := range f.screens {
			fmt.Fprintf(&b, "TAB|%d|t_%03d||pwd=C:\\tab%d\n", i, i, i)
		}
		return b.String()
	case "TAIL":
		idx := f.active
		if len(p) >= 3 {
			if n, err := strconv.Atoi(p[2]); err == nil && n < len(f.screens) {
				idx = n
			}
		}
		return fmt.Sprintf("TAIL|fake|1\n%s", f.screens[idx])
	}
	return "ERR|UNKNOWN\n"
}

// Revival of an "agent exited" window: tab count 1..2 x active tab state x
// other tab state.
func TestWindowRunsSomethingAgainCartesian(t *testing.T) {
	home := t.TempDir()
	prev := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = prev })
	writeLaunchMeta(t, home, "launched")

	states := []string{"shell", "working", "open"}
	for _, activeState := range states {
		for _, other := range append([]string{"none"}, states...) {
			screens := []string{tabScreens[activeState]}
			if other != "none" {
				screens = append(screens, tabScreens[other])
			}
			win := newFakeWindow(t, 0, screens...)
			want := activeState != "shell" || (other != "none" && other != "shell")
			if got := windowRunsSomethingAgain("launched", win.path); got != want {
				t.Errorf("active=%s other=%s: got %v, want %v", activeState, other, got, want)
			}
		}
	}
	if windowRunsSomethingAgain("launched", `\\.\pipe\deckpilot-test-no-such-window`) {
		t.Error("unreachable window must not be revived")
	}
}

// list: windows with 1 / 2 / 3 tabs, a web session and an unreachable pipe.
func TestAddTabs(t *testing.T) {
	one := newFakeWindow(t, 0, tabScreens["open"])
	two := newFakeWindow(t, 1, tabScreens["working"], tabScreens["shell"])
	three := newFakeWindow(t, 2, tabScreens["shell"], tabScreens["open"], tabScreens["working"])
	infos := []sessionInfo{
		{Name: "one", PipePath: one.path},
		{Name: "two", PipePath: two.path},
		{Name: "three", PipePath: three.path},
		{Name: "web", PipePath: two.path, WsURL: "ws://127.0.0.1:1/x"},
		{Name: "gone", PipePath: `\\.\pipe\deckpilot-test-gone`},
		{Name: "nopipe"},
	}
	addTabs(infos)
	got := map[string][]tabInfo{}
	for _, s := range infos {
		got[s.Name] = s.Tabs
	}
	for _, name := range []string{"one", "web", "gone", "nopipe"} {
		if len(got[name]) != 0 {
			t.Errorf("%s: tabs %+v, want none", name, got[name])
		}
	}
	check := func(name string, states []string, active int) {
		tabs := got[name]
		if len(tabs) != len(states) {
			t.Fatalf("%s: %d tabs, want %d", name, len(tabs), len(states))
		}
		for i, s := range states {
			if tabs[i].State != s || tabs[i].Active != (i == active) || tabs[i].Index != i {
				t.Errorf("%s tab %d = %+v, want state %s", name, i, tabs[i], s)
			}
		}
	}
	check("two", []string{"working", "shell"}, 1)
	check("three", []string{"shell", "open", "working"}, 2)
}
