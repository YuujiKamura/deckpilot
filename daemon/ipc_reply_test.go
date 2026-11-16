package daemon

import (
	"errors"
	"strings"
	"testing"
)

// The reply reader must take a whole SHOW --history reply (scrollback, base64,
// one line) and must not report an oversized or broken reply as a silent daemon.
func TestReadReplyLine(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{"short", "OK|abc|idle\n", "OK|abc|idle", ""},
		{"no trailing newline", "OK|abc|idle", "OK|abc|idle", ""},
		{"over bufio default 64KiB", "OK|" + strings.Repeat("A", 200*1024) + "|idle\n", "OK|" + strings.Repeat("A", 200*1024) + "|idle", ""},
		{"several MiB", "OK|" + strings.Repeat("B", 5<<20) + "|idle\n", "OK|" + strings.Repeat("B", 5<<20) + "|idle", ""},
		{"empty: daemon said nothing", "", "", "no response from daemon"},
		{"over the cap: reason is shown", strings.Repeat("C", maxIPCLine+10), "", "read daemon reply"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readReplyLine(strings.NewReader(c.input))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %d bytes, want %d", len(got), len(c.want))
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("pipe broken") }

func TestReadReplyLineReportsReadError(t *testing.T) {
	_, err := readReplyLine(failingReader{})
	if err == nil || !strings.Contains(err.Error(), "pipe broken") {
		t.Fatalf("err = %v, want the underlying read error", err)
	}
}
