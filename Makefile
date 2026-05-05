# deckpilot Makefile
#
# Conventions:
#   - Targets are documented inline; `make help` lists them.
#   - Shell-portable across Git-Bash (MSYS) on Windows and POSIX.

.DEFAULT_GOAL := help

# CP wire-format fixture pact.
# Server-authoritative source lives in the ghostty-win vendor submodule;
# deckpilot vendors a byte-identical copy under docs/ for offline test runs
# (see pipe/wire_format_fixture_test.go and
#  ~/.agents/scratch/cp-wire-format-audit-2026-05-05.md section 3).
#
# Override CP_FIXTURES_SRC if your ghostty-win checkout lives elsewhere.
CP_FIXTURES_SRC ?= $(HOME)/ghostty-win/.dispatch/integration/vendor/zig-control-plane/docs/wire-format-fixtures.txt
CP_FIXTURES_DST := docs/cp-wire-format-fixtures.txt

.PHONY: help sync-fixtures check-fixtures

help: ## List documented make targets.
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

sync-fixtures: ## Copy CP wire-format fixtures from ghostty-win (canonical) into deckpilot.
	@test -f "$(CP_FIXTURES_SRC)" || { echo "ERROR: source not found: $(CP_FIXTURES_SRC)"; echo "Set CP_FIXTURES_SRC=<path> or check out ghostty-win at \$$HOME/ghostty-win."; exit 2; }
	@cp "$(CP_FIXTURES_SRC)" "$(CP_FIXTURES_DST)"
	@echo "synced: $(CP_FIXTURES_SRC) -> $(CP_FIXTURES_DST)"

check-fixtures: ## Fail if vendored CP fixtures drift from the ghostty-win canonical copy.
	@test -f "$(CP_FIXTURES_SRC)" || { echo "ERROR: source not found: $(CP_FIXTURES_SRC)"; exit 2; }
	@diff -u "$(CP_FIXTURES_SRC)" "$(CP_FIXTURES_DST)" >/dev/null || { echo "DRIFT: $(CP_FIXTURES_DST) differs from canonical $(CP_FIXTURES_SRC)"; diff -u "$(CP_FIXTURES_SRC)" "$(CP_FIXTURES_DST)"; echo "Run 'make sync-fixtures' to resync."; exit 1; }
	@echo "ok: $(CP_FIXTURES_DST) matches canonical"
