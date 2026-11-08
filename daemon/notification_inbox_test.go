package daemon

import (
	"testing"
)

func useNotificationTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	previous := daemonUserHome
	daemonUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { daemonUserHome = previous })
	return home
}

func TestNotificationInboxAppendAndLimit(t *testing.T) {
	useNotificationTestHome(t)
	for i := 0; i < 3; i++ {
		if err := appendNotificationEvent(NotificationEvent{Recipient: "orchestrator", Session: string(rune('a' + i)), Status: "idle"}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := ReadNotificationEvents(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Session != "b" || events[1].Session != "c" {
		t.Fatalf("unexpected tail events: %+v", events)
	}
}
