package pipe

import (
	"bufio"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

var fakeSeq atomic.Int64

// fakeSession answers one request per connection with reply(request) and closes,
// like the control plane does for TAIL / HISTORY.
func fakeSession(t *testing.T, reply func(req string) string) string {
	t.Helper()
	// UnixNano alone repeats when two sessions start within one clock tick.
	path := fmt.Sprintf(`\\.\pipe\deckpilot-test-body-%d-%d`, time.Now().UnixNano(), fakeSeq.Add(1))
	l, err := winio.ListenPipe(path, nil)
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
				c.Write([]byte(reply(strings.TrimSpace(req))))
			}()
		}
	}()
	return path
}

func scrollback(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %d %s\n", i, strings.Repeat("x", 40))
	}
	return b.String()
}

// A reply of header + text longer than one 64 KiB read must come back whole,
// for both HISTORY and TAIL, across sizes around the read boundary.
func TestHeaderedRepliesAreReadToTheEnd(t *testing.T) {
	for _, kind := range []string{"HISTORY", "TAIL"} {
		for _, lines := range []int{1, 1000, 1400, 4000, 20000} {
			text := scrollback(lines)
			path := fakeSession(t, func(req string) string {
				return fmt.Sprintf("%s|s|%d\n%s", kind, lines, text)
			})
			c := NewClient(path)
			var got string
			var err error
			if kind == "HISTORY" {
				got, err = c.History()
			} else {
				got, err = c.Tail(lines)
			}
			c.Close()
			if err != nil {
				t.Fatalf("%s %d lines: %v", kind, lines, err)
			}
			if strings.TrimRight(got, "\n") != strings.TrimRight(text, "\n") {
				t.Fatalf("%s %d lines: got %d bytes, want %d", kind, lines, len(got), len(text))
			}
		}
	}
}

func TestOneLineRepliesAreUnchanged(t *testing.T) {
	path := fakeSession(t, func(req string) string { return "PONG|s\n" })
	c := NewClient(path)
	defer c.Close()
	if err := c.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
