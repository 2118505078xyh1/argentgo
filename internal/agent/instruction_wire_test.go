package agent

import (
	"fmt"
	"strings"
	"testing"
)

// snapshotWireTaskTools builds the smallest taskTools that can exercise
// configForPersona / childOptions without a model or a real tool runner.
func snapshotWireTaskTools(cfg *SubagentConfig) *taskTools {
	return &taskTools{
		inner: nopTools{},
		cfg:   cfg,
		// A zero parent is safe: childOptions falls back to DefaultSubagentTurns
		// and needs no other field to render a system prompt.
		parent: Options{},
	}
}

// TestSubagentInstructionSnapshotRenderedIntoChildSystemPrompt checks that a
// parent's resolved snapshot reaches the child prompt in All() order
// (user -> thread -> project) with the inheritance note exactly once.
func TestSubagentInstructionSnapshotRenderedIntoChildSystemPrompt(t *testing.T) {
	snap := BuildSnapshot(
		[]string{"user-one", "user-two"},
		[]string{"project-one"},
		[]string{"thread-one", "thread-two"},
	)
	system := snapshotWireTaskTools(&SubagentConfig{Instruction: &snap}).childOptions("label", "").System

	wantOrder := []string{
		"user-one", "user-two",
		"thread-one", "thread-two", InheritanceNote,
		"project-one",
	}
	cursor := 0
	for _, entry := range wantOrder {
		idx := strings.Index(system[cursor:], entry)
		if idx < 0 {
			t.Fatalf("child prompt missing %q in resolution order (remaining %q); full prompt %q", entry, system[cursor:], system)
		}
		cursor += idx + len(entry)
	}
	if !strings.Contains(system, "Inherited instructions from the parent session:") {
		t.Errorf("child prompt missing the instruction header: %q", system)
	}
	if n := strings.Count(system, InheritanceNote); n != 1 {
		t.Errorf("inheritance note count = %d, want 1; prompt %q", n, system)
	}
}

// TestSubagentInstructionSnapshotSurvivesPersonaLayering checks that the
// resolved snapshot overrides a persona-local one just like SeedContext, on
// both the named-persona and unknown-persona paths, without mutating the
// parent's value.
func TestSubagentInstructionSnapshotSurvivesPersonaLayering(t *testing.T) {
	snap := BuildSnapshot([]string{"user-only"}, nil, []string{"thread-only"})
	want := snap.Inherit()
	resolve := func(name string) (SubagentConfig, bool) {
		if name != "reviewer" {
			return SubagentConfig{}, false
		}
		// A persona-local snapshot must not win over the parent's resolved one.
		personaSnap := BuildSnapshot([]string{"persona-user"}, nil, nil)
		return SubagentConfig{System: "persona system", Instruction: &personaSnap}, true
	}
	tt := snapshotWireTaskTools(&SubagentConfig{
		Instruction:    &snap,
		ResolvePersona: resolve,
	})

	named := tt.configForPersona("reviewer")
	if named.Instruction == nil {
		t.Fatal("named persona layering dropped the instruction snapshot")
	}
	if named.Instruction.Fingerprint() != want.Fingerprint() {
		t.Errorf("named persona snapshot = %+v, want the inherited parent snapshot %+v", named.Instruction, want)
	}
	if named.System != "persona system" {
		t.Errorf("System = %q, want the persona system", named.System)
	}

	unknown := tt.configForPersona("missing")
	if unknown.Instruction == nil || unknown.Instruction.Fingerprint() != want.Fingerprint() {
		t.Errorf("unknown persona changed the snapshot: %+v", unknown.Instruction)
	}

	// Inheritance must not mutate the parent's snapshot.
	if len(snap.Thread) != 1 || snap.Thread[0] != "thread-only" {
		t.Fatalf("parent snapshot was mutated by inheritance: %+v", snap)
	}

	system := tt.childOptions("label", "reviewer").System
	for _, layer := range []string{"user-only", "thread-only", InheritanceNote} {
		if !strings.Contains(system, layer) {
			t.Errorf("persona child prompt missing %q: %q", layer, system)
		}
	}
}

// TestSubagentInstructionSnapshotNilLeavesSystemPromptUnchanged checks the
// default-off path: a nil Instruction renders nothing and leaves the generic
// child system prompt byte-identical.
func TestSubagentInstructionSnapshotNilLeavesSystemPromptUnchanged(t *testing.T) {
	got := snapshotWireTaskTools(&SubagentConfig{}).childOptions("label", "").System
	want := fmt.Sprintf(subagentTemplate, "label")
	if got != want {
		t.Fatalf("nil Instruction changed the system prompt:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, InheritanceNote) || strings.Contains(got, "Inherited instructions") {
		t.Fatalf("nil Instruction still rendered an instruction block: %q", got)
	}
}

// TestSubagentInstructionSnapshotGrandchildHasSingleInheritanceNote follows the
// same cfg -> Options.Subagent -> cfg chain the real Runner uses (see
// newToolset), and checks the inheritance marker appears once at every depth.
func TestSubagentInstructionSnapshotGrandchildHasSingleInheritanceNote(t *testing.T) {
	snap := BuildSnapshot([]string{"user-layer"}, []string{"project-layer"}, []string{"thread-layer"})
	root := snapshotWireTaskTools(&SubagentConfig{Instruction: &snap})

	childOpts := root.childOptions("child", "")
	if childOpts.Subagent == nil || childOpts.Subagent.Instruction == nil {
		t.Fatal("child config did not carry the inherited snapshot")
	}
	if n := strings.Count(childOpts.System, InheritanceNote); n != 1 {
		t.Fatalf("child note count = %d, want 1: %q", n, childOpts.System)
	}

	// The Runner rebuilds its taskTools from Options.Subagent on each level.
	grandOpts := snapshotWireTaskTools(childOpts.Subagent).childOptions("grandchild", "")
	if n := strings.Count(grandOpts.System, InheritanceNote); n != 1 {
		t.Fatalf("grandchild note count = %d, want 1 (no duplicate notes): %q", n, grandOpts.System)
	}
	for _, layer := range []string{"user-layer", "thread-layer", "project-layer"} {
		if !strings.Contains(grandOpts.System, layer) {
			t.Errorf("grandchild prompt missing %q: %q", layer, grandOpts.System)
		}
	}
	if strings.Contains(strings.Join(snap.All(), "\n"), InheritanceNote) {
		t.Fatalf("root snapshot was mutated: %+v", snap)
	}
}

// TestSubagentInstructionSnapshotReachesNewChildSeam checks that every task
// path, which all funnel through newChild -> childOptions, hands the child
// runner an Options carrying the inherited snapshot and the rendered prompt.
func TestSubagentInstructionSnapshotReachesNewChildSeam(t *testing.T) {
	snap := BuildSnapshot([]string{"u"}, nil, []string{"t"})
	var got Options
	cfg := &SubagentConfig{
		Instruction: &snap,
		NewChild: func(opts Options) ChildRunner {
			got = opts
			return &fakeChildRunner{answer: "ok"}
		},
	}
	child := snapshotWireTaskTools(cfg).newChild("label", "")
	if child == nil {
		t.Fatal("newChild returned nil")
	}
	if got.Subagent == nil || got.Subagent.Instruction == nil {
		t.Fatal("child runner did not receive the inherited snapshot")
	}
	if n := strings.Count(got.System, InheritanceNote); n != 1 {
		t.Errorf("child prompt note count = %d, want 1: %q", n, got.System)
	}
	if !strings.Contains(got.System, "u") || !strings.Contains(got.System, "t") {
		t.Errorf("child prompt missing snapshot layers: %q", got.System)
	}
}
