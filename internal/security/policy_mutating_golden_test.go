package security

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDefaultMutatingToolsGoldenManifest 锁住 security 包默认的修改类/只读工具集合。
// 新增 apply_patch 是本次唯一允许的变动；其余条目必须与历史逐字节一致，
// 否则任何工具的审批与路径约束判定都会随之漂移。
func TestDefaultMutatingToolsGoldenManifest(t *testing.T) {
	wantMutating := map[string]bool{
		"write": true, "edit": true, "multi_edit": true, "bash": true,
		"undo": true, "restore": true, "apply_patch": true,
	}
	wantReadOnly := map[string]bool{
		"read": true, "ls": true, "grep": true, "find": true, "glob": true,
		"todo": true, "web_fetch": true, "git_status": true,
	}

	assertExactSet(t, "defaultMutatingTools", defaultMutatingTools, wantMutating)
	assertExactSet(t, "defaultReadOnlyTools", defaultReadOnlyTools, wantReadOnly)
}

func assertExactSet(t *testing.T, label string, got, want map[string]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s size = %d, want %d (got: %v)", label, len(got), len(want), got)
	}
	for name, expected := range want {
		if actual, ok := got[name]; !ok || actual != expected {
			t.Errorf("%s[%q] = %v (present=%v), want %v", label, name, actual, ok, expected)
		}
	}
	for name, actual := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s has unexpected entry %q = %v", label, name, actual)
		}
	}
}

// TestApplyPatchIsMutatingKeepsOtherToolsUnchanged 从公开的判定入口验证每个工具的归类：
// 修改类工具得到 Ask（原因标注 mutating），只读工具得到 Allow。apply_patch 必须落在
// 修改类一侧，且原有工具的判定保持原样。
func TestApplyPatchIsMutatingKeepsOtherToolsUnchanged(t *testing.T) {
	policy := DefaultPolicy(t.TempDir())
	empty := json.RawMessage(`{}`)

	mutating := []string{
		"write", "edit", "multi_edit", "bash", "undo", "restore", "apply_patch",
	}
	for _, name := range mutating {
		result := policy.Evaluate(name, empty)
		if result.Decision != Ask {
			t.Errorf("%s: got %v, want Ask (mutating)", name, result.Decision)
		}
		if !strings.Contains(result.Reason, "mutating") {
			t.Errorf("%s: reason %q does not mark it mutating", name, result.Reason)
		}
	}

	readOnly := []string{
		"read", "ls", "grep", "find", "glob", "todo", "web_fetch", "git_status",
	}
	for _, name := range readOnly {
		if got := policy.Check(name, empty); got != Allow {
			t.Errorf("%s: got %v, want Allow (read-only)", name, got)
		}
	}
}
