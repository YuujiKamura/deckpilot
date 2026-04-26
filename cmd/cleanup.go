package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Cleanup removes hang dumps older than --days (default 3).
func Cleanup(args []string) {
	maxAge := 3 * 24 * time.Hour
	baseDir := defaultHangSnapshotBaseDir()
	dryRun := false

	for i := 0; i < len(args); i++ {
		if args[i] == "--days" && i+1 < len(args) {
			var days int
			fmt.Sscanf(args[i+1], "%d", &days)
			maxAge = time.Duration(days) * 24 * time.Hour
			i++
		} else if args[i] == "--dry-run" {
			dryRun = true
		}
	}

	files, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No hang dumps directory found. Nothing to clean.")
			return
		}
		fmt.Fprintf(os.Stderr, "Error reading dumps: %v\n", err)
		os.Exit(1)
	}

	count := 0
	now := time.Now()
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}

		if now.Sub(info.ModTime()) > maxAge {
			path := filepath.Join(baseDir, f.Name())
			if dryRun {
				fmt.Printf("[dry-run] would delete: %s (%s old)\n", f.Name(), now.Sub(info.ModTime()).Round(time.Hour))
			} else {
				if err := os.Remove(path); err != nil {
					fmt.Fprintf(os.Stderr, "failed to delete %s: %v\n", f.Name(), err)
				} else {
					count++
				}
			}
		}
	}

	if !dryRun {
		fmt.Printf("Cleaned up %d old hang dumps from %s\n", count, baseDir)
	}
}
