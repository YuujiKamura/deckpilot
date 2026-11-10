package pipe

import (
	"os"
	"testing"
)

func TestIsProcessDefinitelyGone(t *testing.T) {
	if IsProcessDefinitelyGone(os.Getpid()) {
		t.Fatal("current process must not be considered gone")
	}
	if !IsProcessDefinitelyGone(2147483647) {
		t.Fatal("invalid PID should be confirmed gone")
	}
}
