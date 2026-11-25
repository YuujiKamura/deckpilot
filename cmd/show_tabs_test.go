package cmd

import (
	"strings"
	"testing"
)

func TestFormatTabListMarksActiveTab(t *testing.T) {
	raw := "LIST_TABS|2|1\r\nTAB|0|t_000||pwd=C:\\gemini|prompt=0|selection=0\nTAB|1|t_001||pwd=C:\\claude|prompt=0|selection=0\n"
	got := formatTabList(raw)
	want := "tabs: 2 (active index: 1)\n  INDEX  TAB ID  CWD\n  0  t_000  C:\\gemini\n* 1  t_001  C:\\claude\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestFormatTabListActiveFirstTab(t *testing.T) {
	got := formatTabList("LIST_TABS|2|0\nTAB|0|t_000||pwd=A\nTAB|1|t_001||pwd=B")
	if !strings.Contains(got, "* 0  t_000  A\n") || !strings.Contains(got, "  1  t_001  B\n") {
		t.Fatalf("active marker wrong:\n%s", got)
	}
}

func TestFormatTabListWithoutPwd(t *testing.T) {
	got := formatTabList("LIST_TABS|1|0\nTAB|0|t_000||prompt=1")
	if got != "tabs: 1 (active index: 0)\n  INDEX  TAB ID  CWD\n* 0  t_000  \n" {
		t.Fatalf("got %q", got)
	}
}

// Lines deckpilot does not understand are shown, not dropped.
func TestFormatTabListPassesUnknownLinesThrough(t *testing.T) {
	got := formatTabList("LIST_TABS|1|0\nTAB|0|t_000||pwd=A\nWARN|something new\n")
	if !strings.Contains(got, "WARN|something new\n") {
		t.Fatalf("unknown line dropped:\n%s", got)
	}
}

func TestFormatTabListEmpty(t *testing.T) {
	if got := formatTabList(""); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := formatTabList("LIST_TABS|0|0\n"); got != "tabs: 0 (active index: 0)\n  INDEX  TAB ID  CWD\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatListTabs(t *testing.T) {
	if got := formatListTabs("s", nil); got != "" {
		t.Fatalf("no tabs: %q", got)
	}
	if got := formatListTabs("s", []listTab{{Index: 0, ID: "t_000", Active: true, State: "open"}}); got != "" {
		t.Fatalf("single tab must print nothing: %q", got)
	}
	got := formatListTabs("ghostty-1", []listTab{
		{Index: 0, ID: "t_000", Pwd: `C:\a`, State: "working"},
		{Index: 1, ID: "t_001", Pwd: `C:\b`, Active: true, State: "shell"},
		{Index: 2, ID: "t_007", State: "unknown"},
	})
	want := "  tabs (* = active; read a tab with: deckpilot show ghostty-1 --tab <TAB-ID>):\n" +
		"     INDEX  TAB ID  STATE  CWD\n" +
		`     0  t_000  working  C:\a` + "\n" +
		`   * 1  t_001  shell    C:\b` + "\n" +
		"     2  t_007  unknown  \n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestParseSendArgs(t *testing.T) {
	cases := []struct {
		args      []string
		name, tab string
		msg       string
		wantErr   bool
	}{
		{[]string{"s", "hello"}, "s", "", "hello", false},
		{[]string{"s", "hello", "world"}, "s", "", "hello world", false},
		{[]string{"s", "--tab", "0", "hello", "world"}, "s", "0", "hello world", false},
		{[]string{"s", "--tab=1", "hi"}, "s", "1", "hi", false},
		{[]string{"s", "--tab", "id=t_001", "hi"}, "s", "id=t_001", "hi", false},
		{[]string{"s", "--tab", "006", "hi"}, "s", "display=t_006", "hi", false},
		{[]string{"s", "--tab=t_006", "hi"}, "s", "id=t_006", "hi", false},
		{[]string{"s", "--tab", "6", "hi"}, "s", "6", "hi", false},
		{[]string{"s", "--tab", "100", "hi"}, "s", "100", "hi", false},
		{[]string{"s", "--tab", "t_100", "hi"}, "s", "id=t_100", "hi", false},
		{[]string{"s", "--tab", "t_006|0", "hi"}, "", "", "", true},
		{[]string{"s", "--tab", "0", ""}, "s", "0", "", false},
		{[]string{"s", "say", "--tab", "0"}, "s", "", "say --tab 0", false},
		{[]string{"s", "--tab", "0"}, "", "", "", true},
		{[]string{"s", "--tab"}, "", "", "", true},
		{[]string{"s", "--tab", "x", "hi"}, "", "", "", true},
		{[]string{"s", "--tab=", "hi"}, "", "", "", true},
		{[]string{"s", "--tab", "0|1", "hi"}, "", "", "", true},
		{[]string{"s"}, "", "", "", true},
	}
	for _, c := range cases {
		name, tab, msg, err := parseSendArgs(append([]string(nil), c.args...))
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v, wantErr=%v", c.args, err, c.wantErr)
			continue
		}
		if err == nil && (name != c.name || tab != c.tab || strings.Join(msg, " ") != c.msg) {
			t.Errorf("%q: got (%q, %q, %q)", c.args, name, tab, strings.Join(msg, " "))
		}
	}
}
