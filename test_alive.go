package main

import (
	"fmt"
	"syscall"
)

func main() {
	pid := 28588
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := syscall.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		fmt.Printf("isProcessAlive(%d) failed: %v\n", pid, err)
		return
	}
	defer syscall.CloseHandle(h)
	fmt.Printf("isProcessAlive(%d) success\n", pid)
}
