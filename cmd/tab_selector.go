package cmd

import (
	"strings"

	"github.com/YuujiKamura/deckpilot/pipe"
)

const tabSelectorHelp = "a zero-based index (6), padded Ghostty number (006), or tab ID (t_006 or id=t_006)"

// cliTabSelector translates the Ghostty label before passing the selector to
// the daemon. Unpadded numbers retain their meaning as zero-based indexes.
func cliTabSelector(tab string) (string, bool) {
	switch {
	case strings.HasPrefix(tab, "t_"):
		tab = "id=" + tab
	case len(tab) == 3 && tab[0] == '0' && pipe.ValidTab(tab):
		tab = "display=t_" + tab
		return tab, true
	}
	return tab, pipe.ValidTab(tab)
}
