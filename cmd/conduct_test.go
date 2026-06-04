package cmd

import (
	"os"
	"reflect"
	"testing"
)

func TestParseChecklist_Basic(t *testing.T) {
	src := `# tasks

- [ ] do foo
- [x] already done
- [ ] do bar baz
not a checkbox
- [ ]  leading space body
`
	got := ParseChecklist(src)
	want := []ChecklistItem{
		{LineIdx: 2, Body: "do foo", Checked: false},
		{LineIdx: 3, Body: "already done", Checked: true},
		{LineIdx: 4, Body: "do bar baz", Checked: false},
		{LineIdx: 6, Body: "leading space body", Checked: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseChecklist mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestParseChecklist_EmptyAndNoMatches(t *testing.T) {
	if got := ParseChecklist(""); len(got) != 0 {
		t.Fatalf("empty: want 0 items, got %d", len(got))
	}
	if got := ParseChecklist("just text\nno checkboxes\n"); len(got) != 0 {
		t.Fatalf("no-match: want 0 items, got %d", len(got))
	}
}

func TestPickNextTodo(t *testing.T) {
	items := []ChecklistItem{
		{LineIdx: 1, Body: "done one", Checked: true},
		{LineIdx: 2, Body: "todo one", Checked: false},
		{LineIdx: 3, Body: "todo two", Checked: false},
	}
	got, ok := PickNextTodo(items)
	if !ok {
		t.Fatal("PickNextTodo: want ok=true")
	}
	if got.LineIdx != 2 || got.Body != "todo one" {
		t.Fatalf("PickNextTodo: got %#v, want LineIdx=2 Body=todo one", got)
	}
}

func TestPickNextTodo_AllDone(t *testing.T) {
	items := []ChecklistItem{
		{LineIdx: 1, Body: "x", Checked: true},
		{LineIdx: 2, Body: "y", Checked: true},
	}
	if _, ok := PickNextTodo(items); ok {
		t.Fatal("PickNextTodo: want ok=false when all checked")
	}
}

func TestPickNextTodo_Empty(t *testing.T) {
	if _, ok := PickNextTodo(nil); ok {
		t.Fatal("PickNextTodo(nil): want ok=false")
	}
}

func TestMarkDispatched_FlipsCheckboxOnExactLine(t *testing.T) {
	src := "a\n- [ ] task one\n- [ ] task two\nz\n"
	got, err := MarkDispatched(src, 1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "a\n- [x] task one\n- [ ] task two\nz\n"
	if got != want {
		t.Fatalf("MarkDispatched line=1:\n got=%q\nwant=%q", got, want)
	}
}

func TestMarkDispatched_PreservesTrailingNewline(t *testing.T) {
	src := "- [ ] only\n"
	got, err := MarkDispatched(src, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "- [x] only\n" {
		t.Fatalf("trailing newline lost: got %q", got)
	}
}

func TestMarkDispatched_NoTrailingNewline(t *testing.T) {
	src := "- [ ] only"
	got, err := MarkDispatched(src, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "- [x] only" {
		t.Fatalf("no-newline form: got %q", got)
	}
}

func TestMarkDispatched_OutOfRange(t *testing.T) {
	if _, err := MarkDispatched("- [ ] x\n", 5); err == nil {
		t.Fatal("want error for out-of-range line")
	}
	if _, err := MarkDispatched("- [ ] x\n", -1); err == nil {
		t.Fatal("want error for negative line")
	}
}

func TestMarkDispatched_NotACheckbox(t *testing.T) {
	if _, err := MarkDispatched("plain line\n", 0); err == nil {
		t.Fatal("want error when line is not an unchecked checkbox")
	}
}

func TestMarkDispatched_AlreadyChecked(t *testing.T) {
	if _, err := MarkDispatched("- [x] done\n", 0); err == nil {
		t.Fatal("want error when line is already checked")
	}
}

func TestParseConductArgs_DefaultsAndOverrides(t *testing.T) {
	got, errMsg := ParseConductArgs([]string{"--file", "/tmp/x.md"})
	if errMsg != "" {
		t.Fatalf("unexpected err: %s", errMsg)
	}
	if got.File != "/tmp/x.md" || got.Agent != "claude" || got.OneShot {
		t.Fatalf("defaults wrong: %#v", got)
	}
	if got.Interval.Seconds() != 30 {
		t.Fatalf("default interval: got %v, want 30s", got.Interval)
	}

	got, errMsg = ParseConductArgs([]string{"--file", "x", "--agent", "gemini", "--interval", "5s", "--one-shot"})
	if errMsg != "" {
		t.Fatalf("unexpected err: %s", errMsg)
	}
	if got.Agent != "gemini" || got.Interval.Seconds() != 5 || !got.OneShot {
		t.Fatalf("overrides wrong: %#v", got)
	}
}

func TestParseConductArgs_Errors(t *testing.T) {
	cases := [][]string{
		{},                                // no --file
		{"--file"},                        // --file missing value
		{"--file", "x", "--agent"},        // --agent missing value
		{"--file", "x", "--interval"},     // --interval missing value
		{"--file", "x", "--interval", "?"}, // bad duration
		{"--file", "x", "--bogus"},        // unknown flag
	}
	for i, args := range cases {
		if _, errMsg := ParseConductArgs(args); errMsg == "" {
			t.Errorf("case %d (%v): want error, got nil", i, args)
		}
	}
}

func TestConductTick_DispatchesAndMarks(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tasks.md"
	src := "- [ ] first task\n- [ ] second task\n"
	if err := writeFileForTest(path, src); err != nil {
		t.Fatal(err)
	}

	var launched [][]string
	stub := func(args []string) { launched = append(launched, args) }

	cfg := ConductArgs{File: path, Agent: "claude", OneShot: true}
	dispatched, err := conductTick(cfg, stub)
	if err != nil {
		t.Fatalf("conductTick: %v", err)
	}
	if !dispatched {
		t.Fatal("expected dispatched=true")
	}
	if len(launched) != 1 || launched[0][0] != "claude" || launched[0][1] != "first task" {
		t.Fatalf("launched=%v, want [[claude first task]]", launched)
	}
	body, _ := readFileForTest(path)
	want := "- [x] first task\n- [ ] second task\n"
	if body != want {
		t.Fatalf("file rewrite:\n got=%q\nwant=%q", body, want)
	}
}

func TestConductTick_NoTodoReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tasks.md"
	if err := writeFileForTest(path, "- [x] all done\n"); err != nil {
		t.Fatal(err)
	}
	var launched [][]string
	stub := func(args []string) { launched = append(launched, args) }
	cfg := ConductArgs{File: path, Agent: "claude", OneShot: true}
	dispatched, err := conductTick(cfg, stub)
	if err != nil {
		t.Fatalf("conductTick: %v", err)
	}
	if dispatched {
		t.Fatal("expected dispatched=false")
	}
	if len(launched) != 0 {
		t.Fatalf("no launches expected, got %v", launched)
	}
}

func TestConductTick_MarksBeforeDispatch(t *testing.T) {
	// If launch panics or the process dies, the file MUST already show
	// the task as checked so the next tick doesn't double-dispatch.
	dir := t.TempDir()
	path := dir + "/tasks.md"
	if err := writeFileForTest(path, "- [ ] crash me\n"); err != nil {
		t.Fatal(err)
	}
	var observedOnLaunch string
	stub := func(args []string) {
		b, _ := readFileForTest(path)
		observedOnLaunch = b
	}
	cfg := ConductArgs{File: path, Agent: "claude", OneShot: true}
	if _, err := conductTick(cfg, stub); err != nil {
		t.Fatalf("conductTick: %v", err)
	}
	if observedOnLaunch != "- [x] crash me\n" {
		t.Fatalf("file at launch-time was %q, want already-checked", observedOnLaunch)
	}
}

func TestConductTick_MissingFile(t *testing.T) {
	cfg := ConductArgs{File: "/definitely/does/not/exist.md", Agent: "claude", OneShot: true}
	if _, err := conductTick(cfg, func([]string) {}); err == nil {
		t.Fatal("want error for missing file")
	}
}

func writeFileForTest(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func readFileForTest(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
