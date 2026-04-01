//go:build ignore

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	winio "github.com/Microsoft/go-winio"
)

func main() {
	pipe := `\\.\pipe\ghostty-winui3-ghostty-14092-14092`
	if len(os.Args) > 1 {
		pipe = os.Args[1]
	}

	data := []byte("\r")
	encoded := base64.StdEncoding.EncodeToString(data)
	msg := fmt.Sprintf("RAW_INPUT|test|%s\n", encoded)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, pipe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte(msg))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	fmt.Printf("resp: %q\n", string(buf[:n]))
}
