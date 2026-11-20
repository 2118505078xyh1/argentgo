package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

type fakeCheckpointSource struct {
	changes []FileChange
	err     error
	lastID  string
	calls   int
}

func (f *fakeCheckpointSource) Diff(id string) ([]FileChange, error) {
	f.lastID = id
	f.calls++
	return f.changes, f.err
}

func TestCheckpointDiffToolMetadata(t *testing.T) {
	tool := newCheckpointDiffTool(nil)
	if got := tool.Name(); got != "checkpoint_diff" {
		t.Fatalf("Name() = %q, want checkpoint_diff", got)
	}
	if tool.Mutating() {
		t.Fatal("checkpoint_diff must be read-only")
	}

	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(tool.Schema()), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("schema type = %q, want object", schema.Type)
	}
	for _, key := range []string{"checkpoint_id", "root"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("schema missing property %q", key)
		}
	}
	required := false
	for _, key := range schema.Required {
		if key == "checkpoint_id" {
			required = true
		}
	}
	if !required {
		t.Error("schema must require checkpoint_id")
	}
}

func TestCheckpointDiffRendersFileChanges(t *testing.T) {
	source := &fakeCheckpointSource{changes: []FileChange{{
		Path:         "src/main.go",
		Status:       "modified",
		BeforeSHA256: "beforehash",
		AfterSHA256:  "afterhash",
		Added:        1,
		Removed:      1,
	}}}
	tool := newCheckpointDiffTool(source)

	out, err := tool.Run(context.Background(), t.TempDir(), []byte(`{"checkpoint_id":"cp-1"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if source.lastID != "cp-1" || source.calls != 1 {
		t.Fatalf("source got id %q, %d call(s); want cp-1, 1", source.lastID, source.calls)
	}

	var decoded checkpointDiffOutput
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, out)
	}
	if decoded.Count != 1 || len(decoded.Files) != 1 {
		t.Fatalf("output = %+v, want one file", decoded)
	}
	got := decoded.Files[0]
	if got.Path != "src/main.go" || got.BeforeSHA256 != "beforehash" ||
		got.AfterSHA256 != "afterhash" || got.Added != 1 || got.Removed != 1 {
		t.Fatalf("file change = %+v, unexpected summary", got)
	}
}

func TestCheckpointDiffAddedAndDeletedFiles(t *testing.T) {
	source := &fakeCheckpointSource{changes: []FileChange{
		FileChangeFromBytes("new.txt", nil, []byte("hello\n")),
		FileChangeFromBytes("gone.txt", []byte("bye\n"), nil),
	}}
	out, err := newCheckpointDiffTool(source).Run(context.Background(), t.TempDir(), []byte(`{"checkpoint_id":""}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var decoded checkpointDiffOutput
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	// Sorted by path: gone.txt before new.txt.
	if len(decoded.Files) != 2 || decoded.Files[0].Path != "gone.txt" || decoded.Files[1].Path != "new.txt" {
		t.Fatalf("files = %+v, want sorted added/deleted entries", decoded.Files)
	}
	if decoded.Files[0].Status != "deleted" || decoded.Files[0].AfterSHA256 != "" ||
		decoded.Files[0].BeforeSHA256 == "" {
		t.Fatalf("deleted entry = %+v", decoded.Files[0])
	}
	if decoded.Files[1].Status != "added" || decoded.Files[1].BeforeSHA256 != "" ||
		decoded.Files[1].AfterSHA256 == "" {
		t.Fatalf("added entry = %+v", decoded.Files[1])
	}
}

func TestCheckpointDiffBoundedOutput(t *testing.T) {
	changes := make([]FileChange, 400)
	for i := range changes {
		changes[i] = FileChange{
			Path:         fmt.Sprintf("pkg/dir-%03d/file-with-a-long-name-%03d.go", i, i),
			BeforeSHA256: strings.Repeat("a", 64),
			AfterSHA256:  strings.Repeat("b", 64),
			Added:        12,
			Removed:      7,
		}
	}
	ctx := context.WithValue(context.Background(), outputLimitKey{}, 512)
	out, err := newCheckpointDiffTool(&fakeCheckpointSource{changes: changes}).Run(ctx, t.TempDir(), []byte(`{"checkpoint_id":"x"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) > 512 {
		t.Fatalf("output = %d bytes, want <= 512", len(out))
	}
	if !utf8.ValidString(out) || !strings.Contains(out, "[truncated]") {
		t.Fatalf("output was not clipped safely: %q", out)
	}
}

func TestCheckpointDiffGracefulUnavailable(t *testing.T) {
	args := []byte(`{"checkpoint_id":"missing"}`)
	out, err := newCheckpointDiffTool(nil).Run(context.Background(), t.TempDir(), args)
	if err != nil {
		t.Fatalf("nil source returned an error: %v", err)
	}
	if !strings.Contains(out, "checkpoint diff unavailable") {
		t.Fatalf("output = %q, want an unavailable message", out)
	}

	source := &fakeCheckpointSource{err: fmt.Errorf("no such session")}
	out, err = newCheckpointDiffTool(source).Run(context.Background(), t.TempDir(), args)
	if err != nil {
		t.Fatalf("source error was returned as a tool error: %v", err)
	}
	if !strings.Contains(out, "checkpoint diff unavailable") || !strings.Contains(out, "no such session") {
		t.Fatalf("output = %q, want unavailable plus the cause", out)
	}
}

func TestCheckpointDiffBadArguments(t *testing.T) {
	_, err := newCheckpointDiffTool(&fakeCheckpointSource{}).Run(
		context.Background(), t.TempDir(), []byte(`{"checkpoint_id":`),
	)
	if err == nil {
		t.Fatal("Run accepted malformed JSON arguments")
	}
}

func TestCheckpointDiffEmptyChangesReportsZero(t *testing.T) {
	out, err := newCheckpointDiffTool(&fakeCheckpointSource{}).Run(
		context.Background(), t.TempDir(), []byte(`{"checkpoint_id":"empty"}`),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var decoded checkpointDiffOutput
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, out)
	}
	if decoded.Count != 0 || len(decoded.Files) != 0 || !strings.Contains(out, `"files":[]`) {
		t.Fatalf("output = %q, want an empty file list", out)
	}
}

func TestSetCheckpointSourceRegistersTool(t *testing.T) {
	registry := New(t.TempDir(), true)
	source := &fakeCheckpointSource{changes: []FileChange{{Path: "a.go", Status: "modified"}}}
	registry.SetCheckpointSource(source)

	found := false
	for _, name := range registry.Names() {
		if name == "checkpoint_diff" {
			found = true
		}
	}
	if !found {
		t.Fatal("SetCheckpointSource did not register checkpoint_diff")
	}
	out, err := registry.Run(context.Background(), "checkpoint_diff", `{"checkpoint_id":"abc"}`)
	if err != nil {
		t.Fatalf("registry.Run: %v", err)
	}
	if !strings.Contains(out, `"path":"a.go"`) {
		t.Fatalf("output = %q, want the source's file", out)
	}
}

func TestDiffLineCounts(t *testing.T) {
	cases := []struct {
		name           string
		before, after  string
		added, removed int
	}{
		{name: "identical", before: "a\nb\n", after: "a\nb\n"},
		{name: "one edit", before: "a\nb\nc\n", after: "a\nb2\nc\n", added: 1, removed: 1},
		{name: "append", before: "a\n", after: "a\nb\nc\n", added: 2},
		{name: "delete", before: "a\nb\nc\n", after: "a\nc\n", removed: 1},
		{name: "empty to content", after: "x\ny\n", added: 2},
		{name: "content to empty", before: "x\ny\n", removed: 2},
		{name: "crlf ignored", before: "a\r\nb\r\n", after: "a\nb\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			added, removed := DiffLineCounts([]byte(tc.before), []byte(tc.after))
			if added != tc.added || removed != tc.removed {
				t.Fatalf("DiffLineCounts = (%d, %d), want (%d, %d)", added, removed, tc.added, tc.removed)
			}
		})
	}
}

func TestFileChangeFromBytesHashesAndStatus(t *testing.T) {
	before := []byte("one\ntwo\n")
	after := []byte("one\nthree\n")
	got := FileChangeFromBytes("f.go", before, after)
	if got.Status != "modified" || got.Added != 1 || got.Removed != 1 {
		t.Fatalf("change = %+v, want modified with 1 added / 1 removed line", got)
	}
	if got.BeforeSHA256 == "" || got.AfterSHA256 == "" || got.BeforeSHA256 == got.AfterSHA256 {
		t.Fatalf("change = %+v, want distinct before/after hashes", got)
	}
}
