package cmd

import (
	"fmt"

	"github.com/YuujiKamura/deckpilot/daemon"
)

// versionVars holds the build-time version variables injected from main.
var versionVars struct {
	Version   string
	Commit    string
	BuildTime string
}

// SetVersionVars is called from main to inject ldflags values.
func SetVersionVars(version, commit, buildTime string) {
	versionVars.Version = version
	versionVars.Commit = commit
	versionVars.BuildTime = buildTime
}

// Version prints build version information to stdout.
func Version(_ []string) {
	fmt.Printf("version: %s\n", versionVars.Version)
	fmt.Printf("commit:  %s\n", versionVars.Commit)
	fmt.Printf("built:   %s\n", versionVars.BuildTime)
	inst := daemon.Instance()
	if inst == "" {
		inst = "default"
	}
	fmt.Printf("instance: %s (daemon pipe %s)\n", inst, daemon.IPCPipePath())
	if v, err := daemon.RunningDaemonVersion(); err == nil {
		fmt.Printf("running daemon: %s\n", v)
	} else {
		fmt.Println("running daemon: none")
	}
}
