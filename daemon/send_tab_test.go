package daemon

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// typingWindow is a fake Ghostty window with two tabs (active: 1) whose
// tabs echo what is typed into them. routes=true behaves like a Ghostty
// that delivers INPUT|...|<tab> to that tab and advertises it; routes=false
// like an older one that types everything into the active tab.
type typingWindow struct {
	path   string
	routes bool
	mu     sync.Mutex
	active int
	line   [2]string   // text typed but not submitted, per tab
	log    [2][]string // submitted lines, per tab
	writes [2][]string // raw bytes delivered, per tab
}

var typingWindowSeq atomic.Int64

func newTypingWindow(t *testing.T, routes bool) *typingWindow {
	t.Helper()
	f := &typingWindow{
		path:   fmt.Sprintf(`\\.\pipe\deckpilot-test-typing-%d-%d`, time.Now().UnixNano(), typingWindowSeq.Add(1)),
		routes: routes,
		active: 1,
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

func (f *typingWindow) screen(i int) string {
	return strings.Join(f.log[i], "\n") + "\n> " + f.line[i] + "\n? for shortcuts\n"
}

func (f *typingWindow) reply(req string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.Split(req, "|")
	switch p[0] {
	case "LIST_TABS":
		return fmt.Sprintf("LIST_TABS|2|%d\nTAB|0|t_000||pwd=C:\\a\nTAB|1|t_001||pwd=C:\\b\n", f.active)
	case "CAPABILITIES":
		feat := ""
		if f.routes {
			feat = "|features=input_tab_routing"
		}
		return "OK|fake|CAPABILITIES|reads=TAIL,LIST_TABS|writes=INPUT,RAW_INPUT" + feat + "\n"
	case "TAIL":
		i := f.active
		if len(p) >= 3 {
			if n, err := strconv.Atoi(p[2]); err == nil && n < 2 {
				i = n
			}
		}
		return "TAIL|fake|1\n" + f.screen(i)
	case "INPUT", "RAW_INPUT":
		data, _ := base64.StdEncoding.DecodeString(p[2])
		i := f.active
		if f.routes && len(p) >= 4 {
			if n, err := strconv.Atoi(p[3]); err == nil && n < 2 {
				i = n
			}
		}
		f.writes[i] = append(f.writes[i], string(data))
		if string(data) == "\r" {
			f.log[i] = append(f.log[i], "ran: "+f.line[i])
			f.line[i] = ""
		} else {
			f.line[i] += string(data)
		}
		return fmt.Sprintf("QUEUED|fake|%s|1\n", p[0])
	}
	return "ERR|UNKNOWN\n"
}

func (f *typingWindow) delivered() [2][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return [2][]string{append([]string(nil), f.writes[0]...), append([]string(nil), f.writes[1]...)}
}

// Every send: tab spec (active / background, by index or id, missing,
// malformed) x terminal (routes input by tab or not) x message (text, slash
// command, bare Enter). The message lands only in the named tab, a
// background tab on a terminal that cannot route is refused before any
// byte is typed, and missing or malformed tabs type nothing.
func TestHandleSendTabCartesian(t *testing.T) {
	type tabSpec struct {
		tab    string
		target int // -1: no such tab, -2: malformed
	}
	specs := []tabSpec{
		{"1", 1}, {"id=t_001", 1}, // active tab
		{"0", 0}, {"id=t_000", 0}, // background tab
		{"5", -1}, {"id=t_009", -1}, // missing
		{"x", -2}, {"0|1", -2}, // malformed
	}
	messages := []string{"hello world", "/compact", ""}
	n := 0
	for _, sp := range specs {
		for _, routes := range []bool{true, false} {
			for _, msg := range messages {
				n++
				name := fmt.Sprintf("tab=%s/routes=%v/msg=%q", sp.tab, routes, msg)
				t.Run(name, func(t *testing.T) {
					win := newTypingWindow(t, routes)
					d := New()
					d.sessions["ghostty-1"] = win.path
					resp := d.handleSendTab([]string{"SENDTAB", "ghostty-1",
						base64.StdEncoding.EncodeToString([]byte(msg)), "caller", sp.tab})
					got := win.delivered()

					refused := sp.target < 0 || (sp.target == 0 && !routes)
					if refused {
						if !strings.HasPrefix(resp, "ERR|") {
							t.Fatalf("want refusal, got %q", resp)
						}
						if len(got[0])+len(got[1]) != 0 {
							t.Fatalf("refused send typed %q", got)
						}
						switch {
						case sp.target == -2 && !strings.Contains(resp, "invalid tab"):
							t.Fatalf("malformed: %q", resp)
						case sp.target == -1 && !strings.Contains(resp, "no tab"):
							t.Fatalf("missing: %q", resp)
						case sp.target == 0 && !strings.Contains(resp, "active tab"):
							t.Fatalf("no routing: %q", resp)
						}
						return
					}

					want := []string{}
					if msg != "" {
						want = append(want, msg)
					}
					want = append(want, "\r")
					if strings.HasPrefix(msg, "/") {
						want = append(want, "\r")
					}
					other := 1 - sp.target
					if strings.Join(got[sp.target], "·") != strings.Join(want, "·") {
						t.Fatalf("tab %d got %q, want %q", sp.target, got[sp.target], want)
					}
					if len(got[other]) != 0 {
						t.Fatalf("tab %d got stray input %q", other, got[other])
					}
					wantPrefix := fmt.Sprintf("OK|submit_ok|tab=%d|", sp.target)
					if msg == "" {
						wantPrefix = fmt.Sprintf("OK|sent|enter|tab=%d", sp.target)
					}
					if !strings.HasPrefix(resp, wantPrefix) {
						t.Fatalf("reply %q, want prefix %q", resp, wantPrefix)
					}
				})
			}
		}
	}
	if n != len(specs)*2*len(messages) {
		t.Fatalf("generated %d cases", n)
	}
}

func TestHandleSendTabUnknownSessionAndUsage(t *testing.T) {
	d := New()
	if resp := d.handleSendTab([]string{"SENDTAB", "nope", "aGk=", "c", "0"}); !strings.Contains(resp, "session not found") {
		t.Fatalf("unknown session: %q", resp)
	}
	if resp := d.handleSendTab([]string{"SENDTAB", "nope", "aGk="}); !strings.HasPrefix(resp, "ERR|usage") {
		t.Fatalf("short request: %q", resp)
	}
	win := newTypingWindow(t, true)
	d.sessions["ghostty-1"] = win.path
	if resp := d.handleSendTab([]string{"SENDTAB", "ghostty-1", "%%%", "c", "0"}); !strings.Contains(resp, "bad base64") {
		t.Fatalf("bad base64: %q", resp)
	}
}

func TestParseSendTabReply(t *testing.T) {
	cases := []struct {
		resp, out string
		ok        bool
		errHas    string
	}{
		{"OK|submit_ok|tab=0|ok_cleared|120ms\n", "submit_ok|tab=0|ok_cleared|120ms", true, ""},
		{"OK|sent|enter|tab=1\n", "sent|enter|tab=1", true, ""},
		{"ERR|unknown command: SENDTAB\n", "", false, "deckpilot shutdown"},
		{"ERR|no tab \"5\" in s\n", "", false, "no tab"},
	}
	for _, c := range cases {
		out, err := parseSendTabReply(c.resp)
		if (err == nil) != c.ok || out != c.out || (err != nil && !strings.Contains(err.Error(), c.errHas)) {
			t.Errorf("parseSendTabReply(%q) = %q, %v", c.resp, out, err)
		}
	}
}

func TestResolveTabIndex(t *testing.T) {
	tabs := parseTabList("LIST_TABS|2|1\nTAB|0|t_000||\nTAB|1|t_004||\n")
	for tab, want := range map[string]int{"0": 0, "1": 1, "id=t_000": 0, "id=t_004": 1, "2": -1, "id=t_001": -1} {
		got, ok := resolveTabIndex(tabs, tab)
		if (want < 0) == ok || (ok && got.Index != want) {
			t.Errorf("resolveTabIndex(%q) = %+v, %v; want %d", tab, got, ok, want)
		}
	}
}
