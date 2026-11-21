package daemon

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

var showTabSeq atomic.Int64

// fakeTabbedTerminal is a control-plane pipe with two tabs (t_000, t_001)
// whose active tab is 1. Like the real terminal, an unknown tab target in
// TAIL / HISTORY falls back to the active tab — the behaviour showTab must
// guard against.
type fakeTabbedTerminal struct {
	path string
	mu   sync.Mutex
	reqs []string
}

func newFakeTabbedTerminal(t *testing.T) *fakeTabbedTerminal {
	t.Helper()
	f := &fakeTabbedTerminal{
		path: fmt.Sprintf(`\\.\pipe\deckpilot-test-showtab-%d-%d`, time.Now().UnixNano(), showTabSeq.Add(1)),
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
				req = strings.TrimSpace(req)
				f.mu.Lock()
				f.reqs = append(f.reqs, req)
				f.mu.Unlock()
				c.Write([]byte(f.reply(req)))
			}()
		}
	}()
	return f
}

func (f *fakeTabbedTerminal) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// tabOf mirrors the server: index or id, unknown -> active tab (1).
func tabOf(field string) int {
	switch field {
	case "0", "id=t_000":
		return 0
	case "1", "id=t_001":
		return 1
	}
	return 1
}

func (f *fakeTabbedTerminal) reply(req string) string {
	p := strings.Split(req, "|")
	switch p[0] {
	case "LIST_TABS":
		return "LIST_TABS|2|1\nTAB|0|t_000||pwd=C:\\gemini|prompt=0|selection=0\nTAB|1|t_001||pwd=C:\\claude|prompt=0|selection=0\n"
	case "TAIL", "HISTORY":
		tab := ""
		if len(p) >= 3 {
			tab = p[2]
		}
		return fmt.Sprintf("%s|fake|1\n%s of tab %d\n", p[0], strings.ToLower(p[0]), tabOf(tab))
	}
	return "ERR|fake|UNKNOWN\n"
}

func decodeShowOK(t *testing.T, resp string) (content, status string) {
	t.Helper()
	body := strings.TrimPrefix(strings.TrimSpace(resp), "OK|")
	parts := strings.SplitN(body, "|", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed OK reply %q", resp)
	}
	raw, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode %q: %v", parts[0], err)
	}
	return string(raw), parts[1]
}

// Every SHOW that names a tab or asks for the tab list, generated as the
// cartesian product mode x tab x session, checked against the rule:
//   - malformed tab              -> checked first, before the session lookup
//   - unknown session            -> session-not-found
//   - mode tabs                  -> the tab list (tab field ignored only when empty)
//   - malformed tab              -> invalid-tab error, nothing sent to the terminal
//   - well-formed but absent tab -> no-tab error, never the active tab's text
//   - existing tab               -> that tab's text, status "live tab=<t>"
func TestHandleShowTabCartesian(t *testing.T) {
	modes := []string{"buffer", "history", "tabs"}
	tabs := []string{"", "0", "1", "id=t_000", "id=t_001", "7", "id=t_999", "-1", "id=", "x"}
	sessions := []string{"known", "unknown"}

	n := 0
	for _, mode := range modes {
		for _, tab := range tabs {
			for _, sess := range sessions {
				if tab == "" && mode != "tabs" {
					continue // active-tab path through the watcher; not showTab's job
				}
				n++
				name := fmt.Sprintf("%s/%s/%q/%s", mode, sess, tab, sess)
				t.Run(name, func(t *testing.T) {
					term := newFakeTabbedTerminal(t)
					d := &Daemon{
						sessions:    map[string]string{},
						wsURLs:      map[string]string{},
						appRuntimes: map[string]string{},
						watchers:    map[string]*Watcher{},
						lastNotify:  map[string]BufferNotification{},
						lastUsed:    map[string]string{},
					}
					if sess == "known" {
						d.sessions["ghostty-1"] = term.path
					}
					parts := []string{"SHOW", "ghostty-1", mode, "caller-x"}
					if tab != "" {
						parts = append(parts, tab)
					}
					resp := d.handleShow(parts)

					valid := tab == "" || validTabForTest(tab)
					exists := tab == "0" || tab == "1" || tab == "id=t_000" || tab == "id=t_001"

					switch {
					case tab != "" && !valid:
						if !strings.HasPrefix(resp, "ERR|invalid tab") {
							t.Fatalf("want invalid-tab, got %q", resp)
						}
						if r := term.requests(); len(r) != 0 {
							t.Fatalf("malformed tab reached the terminal: %q", r)
						}
						return
					case sess == "unknown":
						if !strings.Contains(resp, "session not found") {
							t.Fatalf("want session-not-found, got %q", resp)
						}
						return
					case mode == "tabs":
						// --tabs with a tab is refused by the CLI; the daemon
						// still answers with the list.
						content, status := decodeShowOK(t, resp)
						if !strings.HasPrefix(content, "LIST_TABS|2|1") || strings.Count(content, "\nTAB|") != 2 {
							t.Fatalf("want tab list, got %q", content)
						}
						if status != "live tabs" {
							t.Fatalf("status %q", status)
						}
					case !exists:
						if !strings.HasPrefix(resp, "ERR|no tab") {
							t.Fatalf("want no-tab error, got %q", resp)
						}
						for _, r := range term.requests() {
							if strings.HasPrefix(r, "TAIL") || strings.HasPrefix(r, "HISTORY") {
								t.Fatalf("absent tab was read anyway: %q", r)
							}
						}
					default:
						content, status := decodeShowOK(t, resp)
						kind := "tail"
						if mode == "history" {
							kind = "history"
						}
						want := fmt.Sprintf("%s of tab %d", kind, tabOf(tab))
						if strings.TrimSpace(content) != want {
							t.Fatalf("content %q, want %q", content, want)
						}
						if status != "live tab="+tab {
							t.Fatalf("status %q", status)
						}
						if got, _ := d.getLastUsed("caller-x"); got != "ghostty-1" {
							t.Fatalf("lastUsed not recorded: %q", got)
						}
					}
				})
			}
		}
	}
	// 3 modes x 10 tabs x 2 sessions, minus the 2 modes x 1 empty tab x 2 sessions
	// that take the watcher path.
	if n != 3*10*2-2*2 {
		t.Fatalf("generated %d cases", n)
	}
}

func validTabForTest(tab string) bool {
	switch tab {
	case "0", "1", "7", "id=t_000", "id=t_001", "id=t_999":
		return true
	}
	return false
}

func TestCheckTabReply(t *testing.T) {
	cases := []struct {
		mode, tab, status string
		ok                bool
	}{
		{"buffer", "", "idle", true},
		{"history", "", "active (stale: x)", true},
		{"buffer", "0", "live tab=0", true},
		{"history", "id=t_001", "live tab=id=t_001", true},
		{"buffer", "0", "live tab=1", false},
		{"buffer", "0", "active", false},
		{"history", "1", "idle", false},
		{"tabs", "", "live tabs", true},
		{"tabs", "", "idle", false},
		{"tabs", "", "active", false},
	}
	for _, c := range cases {
		err := checkTabReply(c.mode, c.tab, c.status)
		if (err == nil) != c.ok {
			t.Errorf("checkTabReply(%q,%q,%q) err=%v, want ok=%v", c.mode, c.tab, c.status, err, c.ok)
		}
	}
}
