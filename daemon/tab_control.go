package daemon

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

func checkedTabs(raw string) ([]tabInfo, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	fields := strings.Split(strings.TrimSpace(lines[0]), "|")
	if len(fields) < 3 || fields[0] != "LIST_TABS" {
		return nil, fmt.Errorf("invalid tab list")
	}
	count, err := strconv.Atoi(fields[1])
	if err != nil || count < 1 {
		return nil, fmt.Errorf("invalid tab count")
	}
	tabs := parseTabList(raw)
	ids, indices := map[string]bool{}, map[int]bool{}
	for _, t := range tabs {
		if t.ID == "" || t.Index < 0 || ids[t.ID] || indices[t.Index] {
			return nil, fmt.Errorf("invalid tab list entries")
		}
		ids[t.ID] = true
		indices[t.Index] = true
	}
	if len(tabs) != count {
		return nil, fmt.Errorf("incomplete tab list")
	}
	return tabs, nil
}

func (d *Daemon) handleTab(parts []string) string {
	if len(parts) < 3 || parts[1] == "" {
		return "ERR|usage: TAB|<session>|new, new-safe or close|<tab>\n"
	}
	name, action := parts[1], parts[2]
	path, ok := d.resolvePipePath(name)
	if !ok || path == "" {
		d.refreshSessions()
		path, ok = d.resolvePipePath(name)
		if !ok || path == "" {
			return sessionNotFoundResp(name)
		}
	}
	if action == "new-safe" && !pipe.HasFeature(path, pipe.InputTabRoutingFeature) {
		return "ERR|Ghostty lacks input_tab_routing\n"
	}
	raw, err := pipe.ListTabs(path)
	if err != nil {
		return fmt.Sprintf("ERR|tabs: %s\n", sanitizeErr(err))
	}
	before, err := checkedTabs(raw)
	if err != nil {
		return fmt.Sprintf("ERR|tabs: %s\n", sanitizeErr(err))
	}
	command := "NEW_TAB"
	closedID := ""
	if action == "close" {
		if len(parts) != 4 || !pipe.ValidTab(parts[3]) {
			return "ERR|invalid tab\n"
		}
		if len(before) <= 1 {
			return "ERR|cannot close the last tab\n"
		}
		target, found := resolveTabIndex(before, parts[3])
		if !found {
			return "ERR|tab not found\n"
		}
		closedID = target.ID
		command = fmt.Sprintf("CLOSE_TAB|%d", target.Index)
	} else if action != "new" && action != "new-safe" {
		return "ERR|unknown tab action\n"
	}
	resp, err := pipe.SendRecv(path, command)
	if err != nil {
		return fmt.Sprintf("ERR|control: %s\n", sanitizeErr(err))
	}
	fields := strings.Split(strings.TrimSpace(resp), "|")
	op := strings.Split(command, "|")[0]
	if len(fields) < 3 || (fields[0] != "ACK" && fields[0] != "OK") || fields[2] != op {
		return fmt.Sprintf("ERR|unexpected control reply: %s\n", sanitizeErr(fmt.Errorf("%s", resp)))
	}
	var after []tabInfo
	for i := 0; i < 20; i++ {
		raw, err = pipe.ListTabs(path)
		if err == nil {
			after, err = checkedTabs(raw)
		}
		if err == nil && ((op == "NEW_TAB" && len(after) > len(before)) || (op == "CLOSE_TAB" && len(after) < len(before))) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Sprintf("ERR|tabs after %s: %s\n", op, sanitizeErr(err))
	}
	if op == "NEW_TAB" {
		old := map[string]bool{}
		for _, t := range before {
			old[t.ID] = true
		}
		var added []tabInfo
		for _, t := range after {
			if !old[t.ID] {
				added = append(added, t)
			}
		}
		if len(added) != 1 || len(after) != len(before)+1 {
			return "ERR|cannot identify one new tab; inspect tabs manually\n"
		}
		return fmt.Sprintf("OK|%d|%s\n", added[0].Index, added[0].ID)
	}
	for _, t := range after {
		if t.ID == closedID {
			return "ERR|requested tab remains; inspect tabs manually\n"
		}
	}
	if len(after) != len(before)-1 {
		return "ERR|could not confirm closed tab; inspect tabs manually\n"
	}
	return "OK|" + base64.StdEncoding.EncodeToString([]byte(raw)) + "\n"
}

func DaemonTab(name, action, tab string) (string, error) {
	if name == "" || strings.ContainsAny(name+action+tab, "|\r\n") {
		return "", fmt.Errorf("invalid tab request")
	}
	req := "TAB|" + name + "|" + action
	if tab != "" {
		req += "|" + tab
	}
	resp, err := dialDaemon(req)
	if err != nil {
		return "", err
	}
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "ERR|") {
		return "", errFromResponse(strings.TrimPrefix(resp, "ERR|"))
	}
	if !strings.HasPrefix(resp, "OK|") {
		return "", fmt.Errorf("unexpected daemon response: %s", resp)
	}
	out := strings.TrimPrefix(resp, "OK|")
	if action == "close" {
		data, err := base64.StdEncoding.DecodeString(out)
		return string(data), err
	}
	return out, nil
}
