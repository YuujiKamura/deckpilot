package main

import (
	"fmt"

	"github.com/YuujiKamura/deckpilot/pipe"
)

func main() {
	pipePath := `\\.\pipe\WindowsTerminal-debug9-28588`
	
	fmt.Println("--- TAB 0 ---")
	resp0, err := pipe.SendRecv(pipePath, "TAIL|50|0")
	if err != nil {
		fmt.Println("Err:", err)
	} else {
		fmt.Println(resp0)
	}

	fmt.Println("--- TAB 1 ---")
	resp1, err := pipe.SendRecv(pipePath, "TAIL|50|1")
	if err != nil {
		fmt.Println("Err:", err)
	} else {
		fmt.Println(resp1)
	}
}
