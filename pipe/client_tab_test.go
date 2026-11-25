package pipe

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidTab(t *testing.T) {
	cases := map[string]bool{
		"0":          true,
		"1":          true,
		"12":         true,
		"id=t_001":   true,
		"id=abc-DEF": true,
		"":           false,
		"-1":         false,
		"1.5":        false,
		" 1":         false,
		"id=":        false,
		"id=t|1":     false,
		"0|evil":     false,
		"0\nPING":    false,
		"id=t 1":     false,
		"tab1":       false,
	}
	for in, want := range cases {
		if got := ValidTab(in); got != want {
			t.Errorf("ValidTab(%q) = %v, want %v", in, got, want)
		}
	}
}

// recorder is a fake session that remembers every request line it got.
type recorder struct {
	mu   sync.Mutex
	reqs []string
}

func (r *recorder) add(req string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reqs...)
}

// Every valid tab target reaches the wire as the last field of TAIL / HISTORY,
// and the body comes back with the header stripped.
func TestTailTabAndHistoryTabWireFormat(t *testing.T) {
	for _, tab := range []string{"0", "1", "id=t_001"} {
		for _, kind := range []string{"TAIL", "HISTORY"} {
			rec := &recorder{}
			path := fakeSession(t, func(req string) string {
				rec.add(req)
				return kind + "|s|2\nbody for " + req + "\n"
			})
			var got string
			var err error
			want := ""
			if kind == "TAIL" {
				got, err = TailTab(path, 30, tab)
				want = "TAIL|30|" + tab
			} else {
				got, err = HistoryTab(path, tab)
				want = "HISTORY|0|" + tab
			}
			if err != nil {
				t.Fatalf("%s tab=%s: %v", kind, tab, err)
			}
			reqs := rec.all()
			if len(reqs) != 1 || reqs[0] != want {
				t.Fatalf("%s tab=%s: wire %q, want %q", kind, tab, reqs, want)
			}
			if strings.TrimSpace(got) != "body for "+want {
				t.Fatalf("%s tab=%s: body %q", kind, tab, got)
			}
		}
	}
}

// An invalid tab is refused before anything is written to the pipe.
func TestInvalidTabNeverReachesTheWire(t *testing.T) {
	rec := &recorder{}
	path := fakeSession(t, func(req string) string {
		rec.add(req)
		return "TAIL|s|0\n"
	})
	for _, tab := range []string{"", "-1", "0|PING", "0\nINPUT|x|y", "id="} {
		if _, err := TailTab(path, 10, tab); err == nil {
			t.Errorf("TailTab(%q): want error", tab)
		}
		if _, err := HistoryTab(path, tab); err == nil {
			t.Errorf("HistoryTab(%q): want error", tab)
		}
	}
	if reqs := rec.all(); len(reqs) != 0 {
		t.Fatalf("invalid tabs reached the wire: %q", reqs)
	}
}

func TestTabCallsSurfaceServerErrors(t *testing.T) {
	path := fakeSession(t, func(req string) string { return "ERR|s|NO_TABS\n" })
	if _, err := TailTab(path, 10, "9"); err == nil {
		t.Error("TailTab: want server error")
	}
	if _, err := HistoryTab(path, "9"); err == nil {
		t.Error("HistoryTab: want server error")
	}
	if _, err := ListTabs(path); err == nil {
		t.Error("ListTabs: want server error")
	}
}

// LIST_TABS is one line per tab; a window with many tabs must come back whole.
func TestListTabsReadsEveryTab(t *testing.T) {
	for _, n := range []int{1, 2, 50, 3000} {
		var b strings.Builder
		b.WriteString("LIST_TABS|" + itoa(n) + "|0\n")
		for i := 0; i < n; i++ {
			b.WriteString("TAB|" + itoa(i) + "|t_" + itoa(i) + "||pwd=C:\\some\\long\\working\\directory\\path|prompt=0|selection=0\n")
		}
		reply := b.String()
		path := fakeSession(t, func(req string) string {
			if req != "LIST_TABS" {
				return "ERR|s|UNEXPECTED\n"
			}
			return reply
		})
		got, err := ListTabs(path)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if c := strings.Count(got, "\nTAB|"); c != n {
			t.Fatalf("n=%d: got %d TAB lines (%d bytes of %d)", n, c, len(got), len(reply))
		}
	}
}

func TestTabInList(t *testing.T) {
	list := "LIST_TABS|3|1\nTAB|0|t_000||pwd=C:\\a\nTAB|1|t_001||pwd=C:\\b\nTAB|2|t_007||pwd=C:\\c\n"
	cases := map[string]bool{
		"0": true, "1": true, "2": true, "3": false, "9": false,
		"id=t_000": true, "id=t_007": true, "id=t_002": false, "id=t_00": false,
	}
	for tab, want := range cases {
		if got := TabInList(list, tab); got != want {
			t.Errorf("TabInList(%q) = %v, want %v", tab, got, want)
		}
	}
	if TabInList("", "0") || TabInList("LIST_TABS|0|0\n", "0") {
		t.Error("empty list must contain no tab")
	}
}

func TestListTabsRejectsUnexpectedReply(t *testing.T) {
	path := fakeSession(t, func(req string) string { return "PONG\n" })
	if _, err := ListTabs(path); err == nil {
		t.Fatal("want error for non-LIST_TABS reply")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

// Each tab read is retried while the terminal answers BUSY and gives up
// after the budget: busy N times then answer, for N around the budget, for
// all three reads and both BUSY kinds.
func TestTabReadsRetryBusy(t *testing.T) {
	prevB := busyBackoff
	busyBackoff = time.Millisecond
	t.Cleanup(func() { busyBackoff = prevB })

	reads := map[string]func(path string) (string, error){
		"TAIL":      func(p string) (string, error) { return TailTab(p, 5, "0") },
		"HISTORY":   func(p string) (string, error) { return HistoryTab(p, "0") },
		"LIST_TABS": func(p string) (string, error) { return ListTabs(p) },
	}
	answers := map[string]string{
		"TAIL":      "TAIL|s|1\nok\n",
		"HISTORY":   "HISTORY|s|1\nok\n",
		"LIST_TABS": "LIST_TABS|1|0\nTAB|0|t_000||pwd=A\n",
	}
	for kind, read := range reads {
		for _, busy := range []string{"ERR|BUSY|renderer_locked\n", "ERR|BUSY|data_lane_full\n", "BUSY|renderer_locked\n"} {
			for _, n := range []int{0, 1, busyRetries, busyRetries + 1, busyRetries + 3} {
				var calls atomic.Int64
				path := fakeSession(t, func(req string) string {
					if int(calls.Add(1)) <= n {
						return busy
					}
					return answers[kind]
				})
				_, err := read(path)
				wantOK := n <= busyRetries
				if (err == nil) != wantOK {
					t.Fatalf("%s busy=%q n=%d: err=%v, want ok=%v", kind, busy, n, err, wantOK)
				}
				wantCalls := n + 1
				if !wantOK {
					wantCalls = busyRetries + 1
				}
				if got := int(calls.Load()); got != wantCalls {
					t.Fatalf("%s n=%d: %d calls, want %d", kind, n, got, wantCalls)
				}
			}
		}
	}
}

// A non-busy error is not retried.
func TestTabReadsDoNotRetryOtherErrors(t *testing.T) {
	var calls atomic.Int64
	path := fakeSession(t, func(req string) string {
		calls.Add(1)
		return "ERR|s|NO_TABS\n"
	})
	if _, err := TailTab(path, 5, "0"); err == nil {
		t.Fatal("want error")
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls, want 1", calls.Load())
	}
}

// Input to a tab carries its selector as the last field. Malformed selectors
// are refused before anything is written.
func TestSendToTabWireFormat(t *testing.T) {
	rec := &recorder{}
	path := fakeSession(t, func(req string) string {
		rec.add(req)
		return "QUEUED|s|INPUT|42\n"
	})
	id, err := SendKeysTab(path, "hi there", "1")
	if err != nil || id != 42 {
		t.Fatalf("SendKeysTab: id=%d err=%v", id, err)
	}
	if _, err := SendRawTab(path, []byte("\r"), "id=t_006"); err != nil {
		t.Fatalf("SendRawTab: %v", err)
	}
	want := []string{"INPUT|deckpilot|" + Base64Encode("hi there") + "|1", "RAW_INPUT|deckpilot|DQ==|id=t_006"}
	if got := rec.all(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("wire %q, want %q", got, want)
	}
	for _, bad := range []string{"", "id=", "-1", "0|1", "0\nX"} {
		if _, err := SendKeysTab(path, "x", bad); err == nil {
			t.Errorf("SendKeysTab(%q): want error", bad)
		}
		if _, err := SendRawTab(path, []byte("\r"), bad); err == nil {
			t.Errorf("SendRawTab(%q): want error", bad)
		}
	}
	if n := len(rec.all()); n != 2 {
		t.Fatalf("bad tabs reached the wire (%d requests)", n)
	}
}

func TestHasFeature(t *testing.T) {
	cases := map[string]bool{
		"OK|g|CAPABILITIES|reads=TAIL|features=input_tab_routing\n":     true,
		"OK|g|CAPABILITIES|reads=TAIL|features=a,input_tab_routing,b\n": true,
		"OK|g|CAPABILITIES|reads=TAIL|control=FOCUS\n":                  false,
		"OK|g|CAPABILITIES|features=input_tab_routing_v2\n":             false,
		"OK|g|CAPABILITIES|reads=input_tab_routing\n":                   false,
		"ERR|UNKNOWN\n": false,
	}
	for reply, want := range cases {
		path := fakeSession(t, func(req string) string { return reply })
		if got := HasFeature(path, InputTabRoutingFeature); got != want {
			t.Errorf("HasFeature(%q) = %v, want %v", reply, got, want)
		}
	}
	if HasFeature(`\.\pipe\deckpilot-test-no-such`, InputTabRoutingFeature) {
		t.Error("unreachable terminal must not claim the feature")
	}
}
