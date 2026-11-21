package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A deckpilot instance is a daemon plus the CLI calls that talk to it.
// The default instance is the one everybody uses. Setting
// DECKPILOT_INSTANCE=<name> (e.g. "canary") runs a second, independent
// daemon next to it so a new build can be smoke-tested against the same
// live Ghostty sessions while the current build keeps serving everyone:
//
//   - its own daemon pipe and singleton mutex (neither daemon can take the
//     other over),
//   - its own log file,
//   - no WebSocket bridge unless --ws-port is given (port 8080 belongs to
//     the default instance),
//   - its own idle hooks and notification inbox, so a hook registered on
//     one daemon never fires twice.
//
// Both instances still read the shared launch metadata under ~/.deckpilot,
// which is how they recognise the same sessions.
const instanceEnv = "DECKPILOT_INSTANCE"

var instanceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Instance returns the instance name, or "" for the default instance. A
// value that is not a short lowercase name is ignored so a typo cannot
// produce odd pipe or directory names.
func Instance() string {
	v := os.Getenv(instanceEnv)
	if v == "" || v == "default" || !instanceNamePattern.MatchString(v) {
		return ""
	}
	return v
}

// InstanceInvalid reports a DECKPILOT_INSTANCE value that Instance ignores.
func InstanceInvalid() (string, bool) {
	v := os.Getenv(instanceEnv)
	if v == "" || v == "default" || instanceNamePattern.MatchString(v) {
		return "", false
	}
	return v, true
}

// instanceStateDir is where daemon-owned state (idle hooks, notification
// inbox) lives: ~/.deckpilot for the default instance,
// ~/.deckpilot/instances/<name> otherwise.
func instanceStateDir() string {
	root := ""
	if home, err := daemonUserHome(); err == nil {
		root = filepath.Join(home, ".deckpilot")
	} else {
		root = filepath.Join(os.TempDir(), "deckpilot")
	}
	if inst := Instance(); inst != "" {
		return filepath.Join(root, "instances", inst)
	}
	return root
}

// instanceLogName is the daemon log file name in %TEMP%.
func instanceLogName() string {
	if inst := Instance(); inst != "" {
		return "deckpilot-daemon-" + inst + ".log"
	}
	return "deckpilot-daemon.log"
}

// RunningDaemonVersion asks the daemon of this instance which build it is
// (the VERSION reply: {"version","commit","built_at"}). It does not start a
// daemon; an error means none is answering.
func RunningDaemonVersion() (string, error) {
	resp, err := dialDaemon("VERSION")
	if err != nil {
		return "", err
	}
	resp = strings.TrimSpace(resp)
	if !strings.HasPrefix(resp, "OK|") {
		return "", fmt.Errorf("unexpected VERSION reply: %q", resp)
	}
	return strings.TrimPrefix(resp, "OK|"), nil
}
