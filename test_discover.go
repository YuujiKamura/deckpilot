package main

import (
	"fmt"
	"github.com/YuujiKamura/deckpilot/pipe"
)

func main() {
	sessions, err := pipe.Discover()
	if err != nil {
		fmt.Printf("Discover error: %v\n", err)
	}
	fmt.Printf("Discovered %d sessions\n", len(sessions))
	for _, s := range sessions {
		fmt.Printf("- %s (runtime: %s, pipe: %s)\n", s.Name, s.AppRuntime, s.PipePath)
	}
}
