package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func stubShow(t *testing.T, fn func(n int) (string, string, error)) {
	t.Helper()
	prevShow, prevInterval := daemonShowFn, readyPollInterval
	n := 0
	daemonShowFn = func(string, string, string) (string, string, error) {
		n++
		return fn(n)
	}
	readyPollInterval = time.Millisecond
	t.Cleanup(func() { daemonShowFn, readyPollInterval = prevShow, prevInterval })
}

// Not being able to read the screen is its own outcome, reported after
// maxBlindSamples reads — never folded into "did not become ready".
func TestReadyWaitReportsBlindness(t *testing.T) {
	const menu = "Update available\n  1. Update now\n  2. Skip"
	blind := []struct {
		name string
		show func(n int) (string, string, error)
	}{
		{"daemon-error", func(int) (string, string, error) { return "", "", errors.New("no response from daemon") }},
		{"stale-screen", func(int) (string, string, error) { return menu, "stale:active", nil }},
	}
	for _, agentName := range []string{"codex", "claude", "gemini", "agy"} {
		for _, b := range blind {
			t.Run(agentName+"/"+b.name, func(t *testing.T) {
				calls := 0
				stubShow(t, func(n int) (string, string, error) { calls = n; return b.show(n) })
				started := time.Now()
				_, err := waitForReady("s", agents[agentName], time.Minute)
				if err == nil || !strings.Contains(err.Error(), "cannot read the session screen") {
					t.Fatalf("err = %v, want a cannot-read error", err)
				}
				if calls != maxBlindSamples {
					t.Fatalf("gave up after %d reads, want %d", calls, maxBlindSamples)
				}
				if time.Since(started) > 10*time.Second {
					t.Fatal("blindness was reported only after a long wait")
				}
			})
		}
	}
}

// A blip shorter than maxBlindSamples must not fail the launch, and a screen
// that is readable but not ready is reported as such with the screen kept.
func TestReadyWaitBlipAndUnreadyScreen(t *testing.T) {
	const menu = "Update available\n  1. Update now\n  2. Skip"
	for _, agentName := range []string{"codex", "claude", "gemini", "agy"} {
		agent := agents[agentName]
		for blips := 0; blips < maxBlindSamples; blips++ {
			t.Run(agentName+"/blips="+string(rune('0'+blips))+"/then-ready", func(t *testing.T) {
				stubShow(t, func(n int) (string, string, error) {
					if n <= blips {
						return "", "", errors.New("no response from daemon")
					}
					return "Welcome\n" + agent.ReadyStr, "active", nil
				})
				d, err := waitForReady("s", agent, time.Minute)
				if err != nil || !d.ReadyMatched {
					t.Fatalf("err = %v, ready = %v; want ready", err, d.ReadyMatched)
				}
			})
		}
		t.Run(agentName+"/unready-screen", func(t *testing.T) {
			stubShow(t, func(int) (string, string, error) { return menu, "active", nil })
			d, err := waitForReady("s", agent, 50*time.Millisecond)
			if err == nil {
				t.Fatal("an update menu was accepted as ready")
			}
			if strings.Contains(err.Error(), "cannot read") {
				t.Fatalf("a readable screen was reported as blind: %v", err)
			}
			if d.LastBuffer != menu {
				t.Fatalf("screen was not kept for the caller: %q", d.LastBuffer)
			}
		})
	}
}

func TestPrintLaunchScreen(t *testing.T) {
	long := strings.Repeat("line\n", 40) + "last line"
	cases := []struct {
		name     string
		d        readyWaitDiagnostics
		contains []string
		excludes []string
	}{
		{"never-read", readyWaitDiagnostics{LastShowError: "no response from daemon"},
			[]string{"never read", "no response from daemon"}, []string{"--- screen"}},
		{"blank-buffer", readyWaitDiagnostics{LastBuffer: " \n\n\t\n", LastShowError: "x"},
			[]string{"never read"}, []string{"--- screen"}},
		{"menu-with-blank-rows", readyWaitDiagnostics{LastBuffer: "Update available\n  1. Update now\n\n\n\n\n"},
			[]string{"--- screen of s", "Update available", "1. Update now", "--- end screen ---"}, []string{"never read"}},
		{"long-screen", readyWaitDiagnostics{LastBuffer: long},
			[]string{"last line"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			printLaunchScreen(&out, "s", c.d)
			for _, want := range c.contains {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
			for _, bad := range c.excludes {
				if strings.Contains(out.String(), bad) {
					t.Errorf("output contains %q:\n%s", bad, out.String())
				}
			}
			if n := strings.Count(out.String(), "\n"); n > launchScreenLines+3 {
				t.Errorf("printed %d lines, more than the %d-line screen tail", n, launchScreenLines)
			}
		})
	}
}

// show --tail N must return the last N lines of text, not N blank rows.
func TestTailLinesIgnoresTrailingBlankRows(t *testing.T) {
	texts := map[string][]string{
		"empty":   {},
		"one":     {"a"},
		"several": {"a", "b", "c", "d"},
	}
	paddings := map[string]string{"none": "", "newlines": "\n\n\n", "spaces-and-crlf": "  \r\n \t\r\n"}
	for textName, lines := range texts {
		for padName, pad := range paddings {
			for _, n := range []int{0, 1, 2, 50} {
				t.Run(textName+"/"+padName+"/n="+string(rune('0'+n%10)), func(t *testing.T) {
					got := tailLines(trimScreen(strings.Join(lines, "\n")+pad), n)
					want := lines
					if n > 0 && len(want) > n {
						want = want[len(want)-n:]
					}
					if got != strings.Join(want, "\n") {
						t.Fatalf("got %q, want %q", got, strings.Join(want, "\n"))
					}
				})
			}
		}
	}
}

// The Codex update screen captured on 2026-11-14 (ghostty-6520). Its cursor
// is the Codex ready marker, so "contains the marker" called it ready.
const codexUpdateScreen = "  Update available · 0.167.1 → 0.167.2\n  Release notes: https://github.com/openai/codex/releases/latest\n\n› 1. Update now (runs npm install -g @openai/codex)\n  2. Skip\n  3. Skip until next version\n\n  enter continue · esc skip\n"

func TestLooksLikeMenu(t *testing.T) {
	cursors := []string{"›", "❯", ">", "*"}
	numbering := map[string][2]string{"dot": {"1. ", "2. "}, "paren": {"1) ", "2) "}}
	for _, cur := range cursors {
		for name, n := range numbering {
			for _, onFirst := range []bool{true, false} {
				first, second := "  "+n[0]+"Update now", "  "+n[1]+"Skip"
				if onFirst {
					first = cur + " " + n[0] + "Update now"
				} else {
					second = cur + " " + n[1] + "Skip"
				}
				screen := "Update available\n" + first + "\n" + second + "\n\nenter continue"
				if !looksLikeMenu(screen) {
					t.Errorf("cursor %q %s onFirst=%v: menu not recognised:\n%s", cur, name, onFirst, screen)
				}
			}
		}
	}
	if !looksLikeMenu(codexUpdateScreen) {
		t.Error("the captured Codex update screen is not recognised as a menu")
	}
	notMenus := map[string]string{
		"empty":                    "",
		"codex ready":              "OpenAI Codex\n\n› Ask Codex to do anything\n\n  ? for shortcuts",
		"claude ready":             "Welcome\n\n> \n  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"numbered text, no cursor": "Steps:\n1. Build\n2. Test\n\n› ",
		"single choice":            "› 1. Only option\n",
		"numbers not from one":     "› 2. Second\n  3. Third\n",
		"cursor on prose":          "› 1.5 is the version\n  2 files changed\n",
	}
	for name, screen := range notMenus {
		if looksLikeMenu(screen) {
			t.Errorf("%s: wrongly treated as a menu:\n%s", name, screen)
		}
	}
}

// A menu must never be accepted as ready, for any agent, and must be
// reported quickly rather than after the full ready timeout.
func TestReadyWaitStopsOnMenuAndDoesNotCallItReady(t *testing.T) {
	for _, agentName := range []string{"codex", "claude", "gemini", "agy"} {
		agent := agents[agentName]
		// The ready marker is on screen together with the menu: as the
		// menu's own cursor (codex ›, gemini/agy >) or elsewhere, such as
		// Claude's status line.
		menu := "Update available\n❯ 1. Update now\n  2. Skip\n\n  " + agent.ReadyStr + " on\n"
		if len([]rune(agent.ReadyStr)) == 1 {
			menu = "Update available\n" + agent.ReadyStr + " 1. Update now\n  2. Skip\n"
		}
		if agentName == "codex" {
			menu = codexUpdateScreen
		}
		if !strings.Contains(menu, agent.ReadyStr) {
			t.Fatalf("%s: the fixture must contain the ready marker", agentName)
		}
		t.Run(agentName+"/menu-stays", func(t *testing.T) {
			calls := 0
			stubShow(t, func(n int) (string, string, error) { calls = n; return menu, "active", nil })
			d, err := waitForReady("s", agent, time.Minute)
			if err == nil || d.ReadyMatched {
				t.Fatalf("a menu was accepted as ready (err=%v)", err)
			}
			if !strings.Contains(err.Error(), "menu") {
				t.Fatalf("err = %v, want a menu error", err)
			}
			if calls != menuSamplesBeforeReport {
				t.Fatalf("reported after %d reads, want %d", calls, menuSamplesBeforeReport)
			}
			if d.LastBuffer != menu {
				t.Fatal("the menu screen was not kept for the caller")
			}
		})
		t.Run(agentName+"/menu-then-ready", func(t *testing.T) {
			stubShow(t, func(n int) (string, string, error) {
				if n < menuSamplesBeforeReport {
					return menu, "active", nil
				}
				return "Welcome\n" + agent.ReadyStr + " \n", "active", nil
			})
			d, err := waitForReady("s", agent, time.Minute)
			if err != nil || !d.ReadyMatched {
				t.Fatalf("a menu answered by the user must lead to ready: err=%v", err)
			}
		})
	}
}
