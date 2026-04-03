package cmd

import (
	"bufio"
	"fmt"
	"log"
	"os"

	"github.com/Microsoft/go-winio"
)

func Listen() {
	pipePath := `\\.\pipe\deckpilot-daemon`
	conn, err := winio.DialPipe(pipePath, nil)
	if err != nil {
		log.Fatalf("error: could not connect to daemon: %v", err)
	}
	defer conn.Close()

	// Send LISTEN command with protocol padding
	fmt.Fprintf(conn, "LISTEN||\n")

	// Read and print JSON notifications
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading from daemon: %v\n", err)
	}
}
