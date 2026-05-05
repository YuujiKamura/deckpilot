package pipe

// Fixture-replay tests for the CP wire-format pact (client side).
//
// These tests are the deckpilot half of the pact established in
// vendor/zig-control-plane/docs/wire-format.md. They feed the
// authoritative response bytes from the fixtures file into the
// deckpilot parser surface (IsError, StripTailHeader, ClassifyBusy)
// and assert the documented client-side classification holds.
//
// The fixtures file is vendored from the submodule under
// docs/cp-wire-format-fixtures.txt. Vendoring (option b in the Lane C
// brief, "make sync-fixtures") keeps the file under deckpilot's tree
// so this test runs without a working ghostty-win checkout.
//
// To resync after a server-side fixture change:
//   cp ../../ghostty-win/.dispatch/integration/vendor/zig-control-plane/docs/wire-format-fixtures.txt \
//      docs/cp-wire-format-fixtures.txt
// (until a Makefile target is added — out of scope for this increment).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureBlock holds one parsed block from cp-wire-format-fixtures.txt.
type fixtureBlock struct {
	note     string
	request  string
	response string
}

// decodeFixtureEscapes decodes the `\n`, `\s`, `\\` escape sequences used
// in the fixture file payloads. Mirrors the Zig-side decoder in
// vendor/zig-control-plane/src/main.zig.
func decodeFixtureEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 's':
				b.WriteByte(' ')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// loadFixtures reads and parses the vendored fixtures file. Returns
// blocks in order of appearance.
func loadFixtures(t *testing.T) []fixtureBlock {
	t.Helper()

	// Test runs from the package directory (pipe/), so the vendored
	// fixtures sit at ../docs/cp-wire-format-fixtures.txt.
	path := filepath.Join("..", "docs", "cp-wire-format-fixtures.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixtures: %v", err)
	}

	var blocks []fixtureBlock
	var note, req, resp string
	hasReq, hasResp := false, false

	flush := func() {
		if hasReq && hasResp {
			blocks = append(blocks, fixtureBlock{note: note, request: req, response: resp})
		}
		note, req, resp = "", "", ""
		hasReq, hasResp = false, false
	}

	for raw := range strings.SplitSeq(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "===" {
			flush()
			continue
		}
		if line == "" {
			continue
		}
		if body, ok := strings.CutPrefix(line, "# "); ok {
			// The block's identifying note is the first `# ` line that
			// starts with `verb:` — same convention as the Zig decoder.
			if strings.HasPrefix(body, "verb:") {
				note = body
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, ">>> "); ok {
			req = decodeFixtureEscapes(rest)
			hasReq = true
			continue
		}
		if rest, ok := strings.CutPrefix(line, "<<< "); ok {
			resp = decodeFixtureEscapes(rest)
			hasResp = true
			continue
		}
	}
	flush()

	return blocks
}

func TestFixturesParse(t *testing.T) {
	blocks := loadFixtures(t)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 fixture blocks, got %d", len(blocks))
	}
	for i, want := range []string{"fresh", "stale", "BUSY"} {
		if !strings.Contains(blocks[i].note, want) {
			t.Errorf("block %d: note %q does not contain %q", i, blocks[i].note, want)
		}
	}
}

// TestTailFreshClassification: the fresh TAIL response is documented as
// not-error; StripTailHeader should unwrap to the inner buffer body.
func TestTailFreshClassification(t *testing.T) {
	blocks := loadFixtures(t)
	fresh := blocks[0]

	if msg, isErr := IsError(fresh.response); isErr {
		t.Fatalf("TAIL fresh classified as error (msg=%q); pact requires not-error", msg)
	}
	if k := ClassifyBusy(fresh.response); k != BusyNone {
		t.Errorf("TAIL fresh classified as busy (kind=%d); pact requires BusyNone", k)
	}

	body := StripTailHeader(fresh.response)
	// Inner body is "user@host:~$ " (trailing space, no newline).
	const wantBody = "user@host:~$ "
	if body != wantBody {
		t.Errorf("StripTailHeader: got %q, want %q", body, wantBody)
	}
}

// TestTailStaleClassification: the stale envelope is the documented
// pact load-bearing case. Parser MUST classify `stale:N|TAIL|...` as
// not-error and unwrap to the inner content. If this test fails as a
// crash, the parser cannot survive what the server is already
// emitting — a real drift requiring user approval to fix.
func TestTailStaleClassification(t *testing.T) {
	blocks := loadFixtures(t)
	stale := blocks[1]

	if msg, isErr := IsError(stale.response); isErr {
		t.Fatalf("TAIL stale classified as error (msg=%q); pact requires not-error", msg)
	}
	if k := ClassifyBusy(stale.response); k != BusyNone {
		t.Errorf("TAIL stale classified as busy (kind=%d); pact requires BusyNone", k)
	}

	// StripTailHeader unwraps the first \n-terminated line (the
	// `stale:N|TAIL|<session>|<linecount>` header) and returns the
	// remainder as the buffer body. Same shape as fresh.
	body := StripTailHeader(stale.response)
	const wantBody = "user@host:~$ "
	if body != wantBody {
		t.Errorf("StripTailHeader on stale: got %q, want %q", body, wantBody)
	}
}

// TestTailBusyClassification: the apprt-only BUSY envelope is a
// 2-field ERR shape (audit drift 2b: no <session>). Must classify as
// error AND as BusyRendererLocked despite the unusual field count.
func TestTailBusyClassification(t *testing.T) {
	blocks := loadFixtures(t)
	busy := blocks[2]

	msg, isErr := IsError(busy.response)
	if !isErr {
		t.Fatalf("TAIL BUSY not classified as error; pact requires error")
	}
	// msg is the ERR| prefix stripped; it should still contain
	// BUSY|renderer_locked for the keyword scan.
	if !strings.Contains(msg, "BUSY|renderer_locked") {
		t.Errorf("TAIL BUSY: stripped msg=%q does not contain BUSY|renderer_locked", msg)
	}

	if k := ClassifyBusy(busy.response); k != BusyRendererLocked {
		t.Errorf("TAIL BUSY: ClassifyBusy=%d, want BusyRendererLocked(%d)", k, BusyRendererLocked)
	}

	if IsNoTabs(busy.response) {
		t.Error("TAIL BUSY: misclassified as NO_TABS")
	}
}

// TestFixtureRequestsAreTAIL: sanity that the fixture file did not
// silently get repurposed for a different verb.
func TestFixtureRequestsAreTAIL(t *testing.T) {
	blocks := loadFixtures(t)
	for i, b := range blocks {
		if !strings.HasPrefix(b.request, "TAIL") {
			t.Errorf("block %d: request %q does not start with TAIL", i, b.request)
		}
	}
}
