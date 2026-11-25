package daemon

import (
	"encoding/base64"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/YuujiKamura/deckpilot/pipe"
)

// handleSendTab types a message into one tab of a window and submits it:
// SENDTAB|<name>|<base64 msg>|<caller>|<tab>. It is a separate command from
// SEND so that a daemon without tab support answers "unknown command"
// instead of typing into the active tab.
//
// The tab is checked against LIST_TABS, and a tab other than the active one
// is only written to when the terminal advertises input tab routing: an
// older Ghostty drops the tab field and would type into the active tab,
// which may be another agent (or the caller itself).
func (d *Daemon) handleSendTab(parts []string) string {
	if len(parts) < 5 {
		return "ERR|usage: SENDTAB|<name>|<base64msg>|<caller>|<tab>\n"
	}
	name, msgB64, caller, tab := parts[1], parts[2], parts[3], strings.TrimSpace(parts[4])
	selector, display := splitTabSelector(tab)
	if !pipe.ValidTab(selector) {
		return fmt.Sprintf("ERR|invalid tab %q (want an index like 0 or id=t_001)\n", tab)
	}
	msgBytes, err := base64.StdEncoding.DecodeString(msgB64)
	if err != nil {
		return fmt.Sprintf("ERR|bad base64: %v\n", err)
	}
	msg := string(msgBytes)

	pipePath, ok := d.resolvePipePath(name)
	if !ok {
		d.refreshSessions()
		if pipePath, ok = d.resolvePipePath(name); !ok || pipePath == "" {
			return sessionNotFoundResp(name)
		}
	}

	if err := checkDisplayTabSupport(pipePath, display); err != nil {
		return fmt.Sprintf("ERR|%s\n", err)
	}
	list, err := pipe.ListTabs(pipePath)
	if err != nil {
		return fmt.Sprintf("ERR|tabs: %s\n", sanitizeErr(err))
	}
	target, ok := resolveTabIndex(parseTabList(list), selector)
	if !ok {
		return fmt.Sprintf("ERR|no tab %q in %s (see: deckpilot show %s --tabs)\n", tab, name, name)
	}
	if !target.Active && !pipe.HasFeature(pipePath, pipe.InputTabRoutingFeature) {
		return fmt.Sprintf("ERR|%s's Ghostty types every input into the active tab, and tab %s is not active; "+
			"switch to that tab and send without --tab, or reopen the window on a Ghostty that routes input by tab\n", name, tab)
	}
	idx := strconv.Itoa(target.Index)
	routingTarget := idx
	if display {
		routingTarget = selector
	}

	// Keep the watcher from polling the same pipe while we type (fewer
	// BUSY replies), as handleSend does.
	if w, ok := d.getWatcher(name); ok && w.Status() != "dead" {
		if rc, err := w.PausePolling(2 * time.Second); err == nil {
			defer close(rc)
		} else {
			log.Printf("watcher[%s]: PausePolling failed (%v); sending to tab %s unpaused", name, err, idx)
		}
	}

	tailFn := func() (string, error) { return pipe.TailTab(pipePath, 40, routingTarget) }
	enter := func() error {
		_, err := pipe.SendRawTab(pipePath, []byte("\r"), routingTarget)
		return err
	}

	d.setLastUsed(caller, name)
	if msg == "" {
		if err := enter(); err != nil {
			return fmt.Sprintf("ERR|send: %s\n", sanitizeErr(err))
		}
		return fmt.Sprintf("OK|sent|enter|tab=%s\n", idx)
	}

	// Same phases as handleSend: type, wait for the screen to change, Enter,
	// then confirm the submit from the tab's own screen.
	baseline, _ := tailFn()
	baselineHash := BufHash(baseline)
	if _, err := pipe.SendKeysTab(pipePath, msg, routingTarget); err != nil {
		return fmt.Sprintf("ERR|send text: %s\n", sanitizeErr(err))
	}
	time.Sleep(100 * time.Millisecond)
	preEnterHash := baselineHash
	for i := 0; i < 20; i++ {
		buf, _ := tailFn()
		if h := BufHash(buf); h != baselineHash && buf != "" {
			preEnterHash = h
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err := enter(); err != nil {
		return fmt.Sprintf("ERR|submit enter: %s\n", sanitizeErr(err))
	}
	result := ConfirmSubmit(tailFn, preEnterHash, 2*time.Second, 200*time.Millisecond)
	if strings.HasPrefix(msg, "/") {
		time.Sleep(500 * time.Millisecond)
		_ = enter()
	}

	switch result.Status {
	case SubmitOKCleared:
		return fmt.Sprintf("OK|submit_ok|tab=%s|%s|%dms\n", idx, result.Status, result.ElapsedMs)
	case SubmitFailedStuck:
		return fmt.Sprintf("ERR|no-effect|submit_failed_stuck|tab=%s|%dms\n", idx, result.ElapsedMs)
	case SubmitUnconfirmed:
		return fmt.Sprintf("ERR|submit_unconfirmed|tab=%s|timeout_after_%dms\n", idx, result.ElapsedMs)
	}
	return "ERR|submit_unknown\n"
}

// resolveTabIndex finds the tab an index ("1") or id ("id=t_001") names.
func resolveTabIndex(tabs []tabInfo, tab string) (tabInfo, bool) {
	id, byID := strings.CutPrefix(tab, "id=")
	for _, t := range tabs {
		if byID && t.ID == id {
			return t, true
		}
		if !byID && strconv.Itoa(t.Index) == tab {
			return t, true
		}
	}
	return tabInfo{}, false
}

// DaemonSendTab sends message to one tab of a session and submits it.
func DaemonSendTab(name, message, caller, tab string) (string, error) {
	if strings.ContainsAny(name+caller+tab, "|\r\n") {
		return "", fmt.Errorf("invalid session, caller or tab for send")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(message))
	resp, err := dialDaemon(fmt.Sprintf("SENDTAB|%s|%s|%s|%s", name, encoded, caller, tab))
	if err != nil {
		return "", err
	}
	return parseSendTabReply(resp)
}

// parseSendTabReply turns a SENDTAB reply into the result or an error. An
// "unknown command" reply comes from a daemon built before SENDTAB.
func parseSendTabReply(resp string) (string, error) {
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "ERR|unknown command") {
		return "", fmt.Errorf("the running daemon cannot send to a tab; run 'deckpilot shutdown' and retry")
	}
	if strings.HasPrefix(resp, "ERR|") {
		return "", errFromResponse(strings.TrimPrefix(resp, "ERR|"))
	}
	return strings.TrimPrefix(resp, "OK|"), nil
}
