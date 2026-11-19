package tui

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeFS struct {
	dirs  map[string][]FileInfo
	files map[string]string
}

func (f fakeFS) ReadDir(path string) ([]FileInfo, error) {
	entries, ok := f.dirs[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return entries, nil
}

func (f fakeFS) ReadFile(path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(data), nil
}

func testWorkspaceFS(root string) fakeFS {
	return fakeFS{
		dirs: map[string][]FileInfo{
			root: {
				{Name: ".git", IsDir: true},
				{Name: "node_modules", IsDir: true},
				{Name: "target", IsDir: true},
				{Name: "dist", IsDir: true},
				{Name: "src", IsDir: true},
				{Name: "main.go"},
				{Name: "big.txt"},
			},
			filepath.Join(root, "src"):          {{Name: "a.go"}},
			filepath.Join(root, ".git"):         {{Name: "config"}},
			filepath.Join(root, "target"):       {{Name: "binary"}},
			filepath.Join(root, "dist"):         {{Name: "bundle.js"}},
			filepath.Join(root, "node_modules"): {{Name: "dep"}},
		},
		files: map[string]string{
			filepath.Join(root, "main.go"): "package main\n",
			filepath.Join(root, "big.txt"): strings.Repeat("a line of text\n", 200),
		},
	}
}

func panelText(m *Model, width, height int) string {
	return strings.Join(rowTexts(m.Frame(width, height)), "\n")
}

func TestPanelTogglesAreHiddenByDefaultAndRestoreTheFrame(t *testing.T) {
	root := "/work"
	m := New(Config{Root: root, FS: testWorkspaceFS(root)})
	m.AppendUser("hello")
	before := rowTexts(m.Frame(100, 24))
	if m.panelsVisible() {
		t.Fatal("all panels must start hidden")
	}

	// The three toggles use bytes the decoder previously discarded.
	for _, tc := range []struct {
		input byte
		kind  Kind
	}{
		{0x14, KindCtrlT},
		{0x02, KindCtrlB},
		{0x10, KindCtrlP},
	} {
		keys := (&Decoder{}).Feed([]byte{tc.input})
		if len(keys) != 1 || keys[0].Kind != tc.kind {
			t.Fatalf("byte %#x decoded to %v, want %v", tc.input, keys, tc.kind)
		}
	}

	m.HandleKey(Key{Kind: KindCtrlT})
	m.HandleKey(Key{Kind: KindCtrlB})
	m.HandleKey(Key{Kind: KindCtrlP})
	if !m.showTasks || !m.showWorkspace || !m.showPlan {
		t.Fatal("the three toggles did not turn the panels on")
	}
	shown := panelText(m, 100, 24)
	for _, want := range []string{"TASKS", "WORKSPACE", "PLAN", "no plan submitted"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the shown panels are missing %q:\n%s", want, shown)
		}
	}

	m.HandleKey(Key{Kind: KindCtrlT})
	m.HandleKey(Key{Kind: KindCtrlB})
	m.HandleKey(Key{Kind: KindCtrlP})
	if m.panelsVisible() {
		t.Fatal("the three toggles did not turn the panels off")
	}
	m.SetStatus("") // a toggle status would legitimately change the footer
	after := rowTexts(m.Frame(100, 24))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("default rendering changed:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

func TestEventStreamUpdatesTasksPanel(t *testing.T) {
	m := New(Config{})
	m.HandleKey(Key{Kind: KindCtrlT})

	m.HandleEvent(Event{Kind: EventToolStart, ToolCallID: "1", ToolName: "read", Args: `{"path":"a.go"}`})
	shown := panelText(m, 100, 24)
	if !strings.Contains(shown, taskRunningMark+" read") || !strings.Contains(shown, "a.go") {
		t.Fatalf("tool_start was not shown as running:\n%s", shown)
	}

	m.HandleEvent(Event{Kind: EventToolEnd, ToolCallID: "1", ToolName: "read", DurationMS: 12})
	shown = panelText(m, 100, 24)
	if !strings.Contains(shown, taskOKMark+" read") || !strings.Contains(shown, "12ms") {
		t.Fatalf("tool_end was not shown as ok with its duration:\n%s", shown)
	}

	m.HandleEvent(Event{Kind: EventToolStart, ToolCallID: "2", ToolName: "bash", Args: `{"command":"go test"}`})
	m.HandleEvent(Event{Kind: EventToolEnd, ToolCallID: "2", ToolName: "bash", IsError: true, DurationMS: 3})
	shown = panelText(m, 100, 24)
	if !strings.Contains(shown, taskErrorMark+" bash") {
		t.Fatalf("a failed tool was not shown as an error:\n%s", shown)
	}

	before := m.tasksScroll
	m.HandleKey(Key{Kind: KindPageUp})
	if m.tasksScroll <= before {
		t.Fatal("page up did not scroll the tasks history")
	}

	for i := 0; i < maxTasks+20; i++ {
		m.HandleEvent(Event{Kind: EventToolStart, ToolCallID: "x", ToolName: "read"})
	}
	m.mu.Lock()
	size := len(m.tasks)
	m.mu.Unlock()
	if size > maxTasks {
		t.Fatalf("task history = %d, want at most %d", size, maxTasks)
	}
}

func TestPlanRendersFromToolEvent(t *testing.T) {
	m := New(Config{})
	m.HandleKey(Key{Kind: KindCtrlP})
	if hint := panelText(m, 100, 24); !strings.Contains(hint, "no plan submitted") {
		t.Fatalf("the empty plan hint is missing:\n%s", hint)
	}

	output := "plan (status: draft, updated: 2026-01-01T00:00:00Z):\n" +
		"1. inspect repo\n2. run tests\nplan recorded only; no steps were executed"
	m.HandleEvent(Event{
		Kind:       EventToolEnd,
		ToolName:   planToolName,
		Args:       `{"steps":["inspect repo","run tests"],"status":"draft"}`,
		Output:     output,
		DurationMS: 4,
	})
	shown := panelText(m, 100, 24)
	for _, want := range []string{"PLAN", "draft", "1. inspect repo", "2. run tests", "plan recorded only"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the plan panel is missing %q:\n%s", want, shown)
		}
	}
}

func TestWorkspaceTreeIgnoresDirsAndCapsPreview(t *testing.T) {
	root := "/work"
	m := New(Config{Root: root, FS: testWorkspaceFS(root)})
	m.HandleKey(Key{Kind: KindCtrlB})

	paths := map[string]bool{}
	for _, entry := range m.workspace {
		paths[entry.Path] = true
	}
	for _, want := range []string{"main.go", "big.txt", "src/a.go"} {
		if !paths[want] {
			t.Fatalf("tree is missing %q: %v", want, paths)
		}
	}
	for _, ignored := range []string{".git", ".git/config", "node_modules", "target", "target/binary", "dist", "dist/bundle.js"} {
		if paths[ignored] {
			t.Fatalf("tree contains ignored path %q: %v", ignored, paths)
		}
	}

	index := -1
	for i, entry := range m.workspace {
		if entry.Path == "big.txt" {
			index = i
		}
	}
	m.HandleKey(Key{Kind: KindDown}) // the cursor really moves with a key
	if index < 0 {
		t.Fatal("big.txt was not indexed")
	}
	m.moveWorkspaceCursorLocked(index - m.workspaceCursor)
	if m.previewPath != "big.txt" {
		t.Fatalf("preview path = %q, want big.txt", m.previewPath)
	}
	if len(m.previewLines) != maxPreviewLines {
		t.Fatalf("preview lines = %d, want %d", len(m.previewLines), maxPreviewLines)
	}

	shown := panelText(m, 100, 24)
	for _, want := range []string{"WORKSPACE", "main.go", "big.txt", "1 │ a line of text"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the workspace panel is missing %q:\n%s", want, shown)
		}
	}

	// One oversized single line must still be clipped by the byte cap.
	fs := testWorkspaceFS(root)
	fs.files[filepath.Join(root, "big.txt")] = strings.Repeat("x", maxPreviewBytes*2)
	m2 := New(Config{Root: root, FS: fs})
	m2.HandleKey(Key{Kind: KindCtrlB})
	m2.workspaceCursor = 0
	for i, entry := range m2.workspace {
		if entry.Path == "big.txt" {
			m2.moveWorkspaceCursorLocked(i - m2.workspaceCursor)
		}
	}
	if got := len([]rune(m2.previewLines[0])); got > maxPreviewBytes {
		t.Fatalf("preview is %d bytes, want at most %d", got, maxPreviewBytes)
	}
}

func TestRunConsumesTheEventsChannel(t *testing.T) {
	reader, writer := io.Pipe()
	var out bytes.Buffer
	events := make(chan Event, 4)
	ready := make(chan *Model, 1)
	done := make(chan error, 1)
	opts := Options{
		IO:      &IO{In: reader, Out: &out},
		Events:  events,
		OnReady: func(m *Model) { ready <- m },
	}
	go func() { done <- Run(context.Background(), opts) }()

	var model *Model
	select {
	case model = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never became ready")
	}
	writer.Write([]byte{0x14}) // Ctrl+T
	waitFor(t, "the tasks panel", func() bool {
		model.mu.Lock()
		defer model.mu.Unlock()
		return model.showTasks
	})
	events <- Event{Kind: EventToolStart, ToolCallID: "t", ToolName: "read", Args: `{"path":"a.go"}`}
	waitFor(t, "the tool event", func() bool {
		model.mu.Lock()
		defer model.mu.Unlock()
		return len(model.tasks) == 1
	})
	close(events)
	writer.Write([]byte{0x03})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctrl+c")
	}
	if !strings.Contains(out.String(), "read") {
		t.Fatalf("the task was not painted: %q", out.String())
	}
}
