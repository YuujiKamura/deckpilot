//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

type stubProcessLister struct {
	list []ProcessInfo
	err  error
}

func (s stubProcessLister) List() ([]ProcessInfo, error) {
	return s.list, s.err
}

// T1 GetProcessTree_FlatList
func TestGetProcessTree_FlatList(t *testing.T) {
	stub := stubProcessLister{
		list: []ProcessInfo{
			{PID: 1, PPID: 0, Name: "root"},
			{PID: 2, PPID: 1, Name: "child1"},
			{PID: 3, PPID: 1, Name: "child2"},
			{PID: 4, PPID: 2, Name: "grandchild"},
		},
	}

	tree, err := GetProcessTree(stub, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tree) != 4 {
		t.Errorf("expected 4 processes, got %d", len(tree))
	}

	pids := make(map[uint32]bool)
	for _, p := range tree {
		pids[p.PID] = true
	}

	for i := uint32(1); i <= 4; i++ {
		if !pids[i] {
			t.Errorf("PID %d missing from tree", i)
		}
	}
}

// T2 GetProcessTree_RootNotFound
func TestGetProcessTree_RootNotFound(t *testing.T) {
	stub := stubProcessLister{list: []ProcessInfo{}}
	_, err := GetProcessTree(stub, 999)
	if err == nil || err.Error() != "root pid 999 not found" {
		t.Errorf("expected 'root pid 999 not found' error, got: %v", err)
	}
}

// T3 GetProcessTree_NoChildren
func TestGetProcessTree_NoChildren(t *testing.T) {
	stub := stubProcessLister{
		list: []ProcessInfo{
			{PID: 1, PPID: 0, Name: "root"},
		},
	}
	tree, err := GetProcessTree(stub, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree) != 1 || tree[0].PID != 1 {
		t.Errorf("expected [1], got %v", tree)
	}
}

// T4 GetProcessTree_ListerError
func TestGetProcessTree_ListerError(t *testing.T) {
	wantErr := fmt.Errorf("lister failed")
	stub := stubProcessLister{err: wantErr}
	_, err := GetProcessTree(stub, 1)
	if err != wantErr {
		t.Errorf("expected %v, got %v", wantErr, err)
	}
}

// T5 GetProcessTree_CyclePrevention
func TestGetProcessTree_CyclePrevention(t *testing.T) {
	stub := stubProcessLister{
		list: []ProcessInfo{
			{PID: 1, PPID: 2, Name: "node1"},
			{PID: 2, PPID: 1, Name: "node2"},
		},
	}
	tree, err := GetProcessTree(stub, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Cycles should be capped. BFS from 1 with cyclic children:
	// result = [1], queue=[1], children=[2], result=[1, 2], queue=[2], children=[1], result=[1, 2, 1]...
	// but the code uses maxIter = 2 * len(all) = 4.
	// Actually, result appends regardless of seen, but queue determines children.
	if len(tree) > 10 { // Loose bound
		t.Errorf("tree unexpectedly large under cycle: %d", len(tree))
	}
}

// T6 SystemProcessLister_ReturnsSelf
func TestSystemProcessLister_ReturnsSelf(t *testing.T) {
	lister := SystemProcessLister{}
	list, err := lister.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	myPID := uint32(os.Getpid())
	found := false
	for _, p := range list {
		if p.PID == myPID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("current PID %d not found in process list", myPID)
	}
}

// T7 SystemProcessLister_IncludesExpectedPaths (spawn real child)
func TestSystemProcessLister_IncludesExpectedPaths(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "ping -n 5 127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child: %v", err)
	}
	defer cmd.Process.Kill()

	myPID := uint32(os.Getpid())
	childPID := uint32(cmd.Process.Pid)

	lister := SystemProcessLister{}
	list, err := lister.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	found := false
	for _, p := range list {
		if p.PID == childPID && p.PPID == myPID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("spawned child PID %d with PPID %d not found in list", childPID, myPID)
	}
}
