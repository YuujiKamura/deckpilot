package daemon

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"github.com/Microsoft/go-winio"
	"strings"
	"testing"
	"time"
)

func TestCheckedTabsAndResolveID(t *testing.T) {
	raw := "LIST_TABS|2|1\nTAB|0|t_000||pwd=C:\\a\nTAB|1|t_001||pwd=C:\\b\n"
	tabs, err := checkedTabs(raw)
	if err != nil {
		t.Fatal(err)
	}
	tab, ok := resolveTabIndex(tabs, "id=t_001")
	if !ok || tab.Index != 1 || !tab.Active {
		t.Fatalf("wrong tab: %+v", tab)
	}
	if _, err := checkedTabs("LIST_TABS|2|0\nTAB|0|t_000\n"); err == nil {
		t.Fatal("accepted incomplete list")
	}
}
func TestSafeSendRefusesActiveTabWithoutRouting(t *testing.T) {
	win := newTypingWindow(t, false)
	d := New()
	d.sessions["ghostty-1"] = win.path
	resp := d.handleSendTab([]string{"SENDTABSAFE", "ghostty-1", base64.StdEncoding.EncodeToString([]byte("hello")), "caller", "1"})
	if !strings.HasPrefix(resp, "ERR|") {
		t.Fatal(resp)
	}
	writes := win.delivered()
	if len(writes[0])+len(writes[1]) != 0 {
		t.Fatal(writes)
	}
}

func TestLastTabCannotClose(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\deckpilot-last-tab-%d`, time.Now().UnixNano())
	l, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			req, _ := bufio.NewReader(c).ReadString('\n')
			_ = req
			fmt.Fprint(c, "LIST_TABS|1|0\nTAB|0|t_000\n")
			c.Close()
		}
	}()
	d := New()
	d.sessions["ghostty-1"] = path
	resp := d.handleTab([]string{"TAB", "ghostty-1", "close", "id=t_000"})
	if !strings.Contains(resp, "last tab") {
		t.Fatal(resp)
	}
}
