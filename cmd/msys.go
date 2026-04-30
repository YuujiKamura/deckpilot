package cmd

import (
	"runtime"
	"strings"
)

// msys2PathPrefixes are known MSYS2 path translation targets.
var msys2PathPrefixes = []string{
	"C:/Program Files/Git/",
	"C:\\Program Files\\Git\\",
}

// unmangleMSYS reverses MSYS2/Git Bash automatic path conversion.
// e.g. "C:/Program Files/Git/usage" -> "/usage"
// Only active on Windows. No-op on other platforms.
func unmangleMSYS(s string) string {
	if runtime.GOOS != "windows" {
		return s
	}
	for _, prefix := range msys2PathPrefixes {
		if strings.HasPrefix(s, prefix) {
			return "/" + s[len(prefix):]
		}
	}
	return s
}
