package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestLaunchIntoUsesOnlyTargetTab(t *testing.T) {
	oldNew, oldShow, oldSend, oldPoll := launchTabNew, launchTabShow, launchTabSend, readyPollInterval
	defer func() {
		launchTabNew, launchTabShow, launchTabSend, readyPollInterval = oldNew, oldShow, oldSend, oldPoll
	}()
	readyPollInterval = time.Millisecond
	launchTabNew = func(s, a, tb string) (string, error) {
		if s != "test-session" || a != "new-safe" {
			t.Fatalf("new: %s %s", s, a)
		}
		return "1|t_001", nil
	}
	var sent []string
	launchTabShow = func(s, mode, caller, tab string) (string, string, error) {
		if s != "test-session" || tab != "id=t_001" {
			t.Fatalf("wrong show target: %s %s", s, tab)
		}
		if len(sent) == 0 {
			return "C:\\test>", "live tab=" + tab, nil
		}
		return "READY", "live tab=" + tab, nil
	}
	launchTabSend = func(s, msg, caller, tab string) (string, error) {
		if s != "test-session" || tab != "id=t_001" {
			t.Fatalf("wrong send target: %s %s", s, tab)
		}
		sent = append(sent, msg)
		return "ok", nil
	}
	err := launchInto(LaunchArgs{Into: "test-session", Cwd: `C:\test`, AgentName: "test", Prompt: "hello", SharedCwd: true, ReadyTimeout: time.Second}, AgentDef{Cmd: "test", ReadyStr: "READY"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || !strings.Contains(sent[0], "cd /d") || sent[1] != "hello" {
		t.Fatalf("sent: %q", sent)
	}
}
