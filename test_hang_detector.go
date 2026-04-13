package main

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
	"github.com/YuujiKamura/deckpilot/daemon"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPeekMessageW     = user32.NewProc("PeekMessageW")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
)

type Msg struct {
	Hwnd    syscall.Handle
	Message uint32
	Wparam  uintptr
	Lparam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

func main() {
	// UIスレッドを固定
	runtime.LockOSThread()

	// 1. ダミーウィンドウを作成
	className, _ := syscall.UTF16PtrFromString("Static")
	windowName, _ := syscall.UTF16PtrFromString("Deckpilot Hang Test")
	
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0x00000000, // WS_OVERLAPPED
		100, 100, 200, 200,
		0, 0, 0, 0)
	
	if hwnd == 0 {
		fmt.Printf("Failed to create window: %v\n", err)
		return
	}
	h := syscall.Handle(hwnd)
	fmt.Printf("Dummy window created: HWND=0x%x\n", h)

	// 2. 監視用のループを別ゴルーチンで回す
	stopCh := make(chan struct{})
	go func() {
		fmt.Println("Starting status monitoring...")
		start := time.Now()
		for i := 0; i < 20; i++ {
			hung := daemon.IsHungAppWindow(h)
			elapsed := time.Since(start).Seconds()
			status := "ALIVE"
			if hung {
				status = "HUNG (STALLED)"
			}
			fmt.Printf("[%4.1fs] Window status: %s\n", elapsed, status)
			time.Sleep(1 * time.Second)
		}
		close(stopCh)
	}()

	// 3. 最初の5秒間はメッセージを正しく処理する（生存アピール）
	fmt.Println("Phase 1: Window is ALIVE (processing messages)")
	deadline := time.Now().Add(5 * time.Second)
	var msg Msg
	for time.Now().Before(deadline) {
		ret, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1) // PM_REMOVE
		if ret != 0 {
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 4. 次の10秒間はメッセージを「わざと処理しない」（ハングさせる）
	fmt.Println("Phase 2: Window is HANGING (ignoring messages)")
	fmt.Println("Expect OS to detect hang after ~5 seconds...")
	time.Sleep(12 * time.Second)

	// 5. 最後にメッセージ処理を再開してみる（復活アピール）
	fmt.Println("Phase 3: Resuming message processing (Revival)")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ret, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1)
		if ret != 0 {
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
		time.Sleep(10 * time.Millisecond)
	}

	<-stopCh
	procDestroyWindow.Call(hwnd)
	fmt.Println("Test finished.")
}
