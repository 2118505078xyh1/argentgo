package memory

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstructionDigest(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	a := []Doc{
		{Path: filepath.Join(root, "AGENTS.md"), Text: "rules v1"},
		{Path: filepath.Join(root, "CLAUDE.md"), Text: "shared"},
	}
	b := []Doc{
		{Path: filepath.Join(root, "AGENTS.md"), Text: "rules v1"},
		{Path: filepath.Join(root, "CLAUDE.md"), Text: "shared"},
	}
	c := []Doc{
		{Path: filepath.Join(root, "AGENTS.md"), Text: "rules v2"},
		{Path: filepath.Join(root, "CLAUDE.md"), Text: "shared"},
	}

	digest := func(docs []Doc) string { return SnapshotInstructions(docs).Version() }

	if got, want := digest(a), digest(b); got != want {
		t.Fatalf("identical doc sets should share a digest: %q != %q", got, want)
	}
	if got, want := digest(a), digest(c); got == want {
		t.Fatalf("changed content must change the digest, got %q", got)
	}
	if got := digest(nil); got != "" {
		t.Fatalf("empty set digest = %q, want empty", got)
	}

	// 顺序不影响集合摘要；路径与正文一起参与摘要，避免交换内容却同样命中。
	swapped := []Doc{{Path: b[1].Path, Text: b[1].Text}, {Path: b[0].Path, Text: b[0].Text}}
	if got, want := digest(swapped), digest(a); got != want {
		t.Fatalf("order must not affect digest: %q != %q", got, want)
	}
	moved := []Doc{{Path: b[0].Path, Text: b[1].Text}, {Path: b[1].Path, Text: b[0].Text}}
	if got, want := digest(moved), digest(a); got == want {
		t.Fatal("swapping contents between paths must change the digest")
	}
}

func TestDiffInstructionStates(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	p := filepath.Join(root, "AGENTS.md")
	q := filepath.Join(root, "CLAUDE.md")

	before := SnapshotInstructions([]Doc{{Path: p, Text: "old rules"}, {Path: q, Text: "removed file"}})
	after := SnapshotInstructions([]Doc{{Path: p, Text: "new rules"}, {Path: filepath.Join(root, "NEW.md"), Text: "new file"}})

	changes := DiffInstructionStates(before, after)
	if len(changes) != 3 {
		t.Fatalf("changes = %#v, want 3 changes", changes)
	}

	got := map[string]InstructionChangeKind{}
	texts := map[string]string{}
	for _, c := range changes {
		got[filepath.Base(c.Path)] = c.Kind
		texts[filepath.Base(c.Path)] = c.Text
	}
	want := map[string]InstructionChangeKind{
		"NEW.md":    InstructionAdded,
		"AGENTS.md": InstructionUpdated,
		"CLAUDE.md": InstructionRemoved,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("change kinds = %#v, want %#v", got, want)
	}
	if texts["AGENTS.md"] != "new rules" || texts["NEW.md"] != "new file" {
		t.Fatalf("new texts not carried in changes: %#v", texts)
	}
	if texts["CLAUDE.md"] != "" {
		t.Fatalf("removed change should carry no text, got %q", texts["CLAUDE.md"])
	}

	// 内容未变时无变化；空基线到非空集合只产生 added。
	if changes := DiffInstructionStates(after, after); len(changes) != 0 {
		t.Fatalf("identical states should yield no changes, got %#v", changes)
	}
	if changes := DiffInstructionStates(InstructionState{}, after); len(changes) != 2 {
		t.Fatalf("empty baseline should yield only additions, got %#v", changes)
	}
}

func TestSnapshotInstructionsClipsLongText(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo")
	long := strings.Repeat("汉", maxInstructionTextBytes)
	state := SnapshotInstructions([]Doc{{Path: filepath.Join(root, "AGENTS.md"), Text: long}})
	changes := DiffInstructionStates(InstructionState{}, state)
	if len(changes) != 1 {
		t.Fatalf("changes = %#v", changes)
	}
	if len(changes[0].Text) > maxInstructionTextBytes {
		t.Fatalf("text not clipped: %d bytes", len(changes[0].Text))
	}
}
