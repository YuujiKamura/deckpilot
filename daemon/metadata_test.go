package daemon

import (
	"strings"
	"testing"
)

func TestQuotaRegex(t *testing.T) {
	line := "5h:86%(resets 1pm) wk:41%(resets Mon) sn:9%(resets May 1)"
	matches := quotaRegex.FindStringSubmatch(line)
	if len(matches) < 7 {
		t.Fatalf("expected 7 matches, got %d", len(matches))
	}
	if matches[1] != "86" || matches[2] != "resets 1pm" {
		t.Errorf("5h mismatch: %s (%s)", matches[1], matches[2])
	}
	if matches[3] != "41" || matches[4] != "resets Mon" {
		t.Errorf("wk mismatch: %s (%s)", matches[3], matches[4])
	}
	if matches[5] != "9" || matches[6] != "resets May 1" {
		t.Errorf("sn mismatch: %s (%s)", matches[5], matches[6])
	}
}

func TestThresholdInvariant(t *testing.T) {
	notified := 0
	w := &Watcher{
		name: "test",
		onNotify: func(n BufferNotification) {
			if strings.HasPrefix(n.Content, "quota warn: test 5h=") {
				notified++
			}
		},
	}

	// 1. Cross threshold
	w.scrapeMetadata("5h:90%(resets 1pm) wk:41%(resets Mon) sn:9%(resets May 1)")
	if notified != 1 {
		t.Errorf("expected 1 notification, got %d", notified)
	}

	// 2. Stay above threshold (no new notification)
	w.scrapeMetadata("5h:91%(resets 1pm) wk:41%(resets Mon) sn:9%(resets May 1)")
	if notified != 1 {
		t.Errorf("expected still 1 notification, got %d", notified)
	}

	// 3. Drop below
	w.scrapeMetadata("5h:89%(resets 1pm) wk:41%(resets Mon) sn:9%(resets May 1)")
	if notified != 1 {
		t.Errorf("expected still 1 notification, got %d", notified)
	}

	// 4. Cross again
	w.scrapeMetadata("5h:95%(resets 1pm) wk:41%(resets Mon) sn:9%(resets May 1)")
	if notified != 2 {
		t.Errorf("expected 2 notifications, got %d", notified)
	}
}

func TestModelDetectionPriority(t *testing.T) {
	d := New()
	name := "test-session"
	d.sessions[name] = "pipe"

	// (a) Launcher flag set
	d.TagSession(name, map[string]string{"model": "opus"})
	if d.models[name] != "opus" {
		t.Errorf("expected opus, got %s", d.models[name])
	}

	// (b) Auto-detect should NOT override manual/flag
	d.TagSession(name, map[string]string{"model": "haiku", "auto": "true"})
	if d.models[name] != "opus" {
		t.Errorf("expected opus to persist, got %s", d.models[name])
	}

	// (c) Manual override should work
	d.TagSession(name, map[string]string{"model": "sonnet"})
	if d.models[name] != "sonnet" {
		t.Errorf("expected sonnet override, got %s", d.models[name])
	}
}
