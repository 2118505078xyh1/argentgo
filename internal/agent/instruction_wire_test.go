package agent

import (
	"fmt"
	"strings"
	"testing"
)

// snapshotWireTaskTools 构建最小的 taskTools，可在没有模型或真实工具运行器的情况下演练 configForPersona / childOptions。
// （接上行）
func snapshotWireTaskTools(cfg *SubagentConfig) *taskTools {
	return &taskTools{
		inner: nopTools{},
		cfg:   cfg,
		// 零值父级是安全的：childOptions 回退到 DefaultSubagentTurns，渲染系统提示不需要其他字段。
		// （接上行）
		parent: Options{},
	}
}

// TestSubagentInstructionSnapshotRenderedIntoChildSystemPrompt 检查父级已解析的快照按 All() 顺序（user -> thread -> project）到达子提示，
// 且继承标记恰好出现一次。
// （接上行）
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

// TestSubagentInstructionSnapshotSurvivesPersonaLayering 检查已解析的快照像 SeedContext 一样覆盖角色自带的快照，
// 在具名角色和未知角色两条路径上都如此，且不修改父级的值。
// （接上行）
// （接上行）
func TestSubagentInstructionSnapshotSurvivesPersonaLayering(t *testing.T) {
	snap := BuildSnapshot([]string{"user-only"}, nil, []string{"thread-only"})
	want := snap.Inherit()
	resolve := func(name string) (SubagentConfig, bool) {
		if name != "reviewer" {
			return SubagentConfig{}, false
		}
		// 角色自带的快照不得优先于父级已解析的快照。
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

	// 继承不得修改父级的快照。
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

// TestSubagentInstructionSnapshotNilLeavesSystemPromptUnchanged 检查默认关闭路径：nil 的 Instruction 不渲染任何内容，
// 使通用子系统提示逐字节不变。
// （接上行）
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

// TestSubagentInstructionSnapshotGrandchildHasSingleInheritanceNote 遵循真实 Runner 所用的同一 cfg -> Options.Subagent -> cfg 链（见 newToolset），
// 并检查继承标记在每个深度只出现一次。
// （接上行）
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

	// Runner 在每一层都从 Options.Subagent 重建其 taskTools。
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

// TestSubagentInstructionSnapshotReachesNewChildSeam 检查所有 task 路径（都经由 newChild -> childOptions）都把带有继承快照和渲染后提示的 Options
// 交给子运行器。
// （接上行）
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
