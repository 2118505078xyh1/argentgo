package hooks

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// TestDelegatedPolicySnapshotFull 校验完整快照：三个权限键与来源标记都在，
// 且无关键不会被带入。
func TestDelegatedPolicySnapshotFull(t *testing.T) {
	parent := map[string]string{
		"sandbox":  "workspace-write",
		"approval": "ask",
		"preset":   "coding",
		"prompt":   "这是不应扩散的原始提示词",
		"api_key":  "sk-proj-secret",
		"cwd":      "/home/user",
		"source":   "user-config",
	}

	got := delegatedPolicySnapshot(parent)
	want := map[string]string{
		"sandbox":  "workspace-write",
		"approval": "ask",
		"preset":   "coding",
		"source":   policySourceDelegated,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("完整快照 = %#v, want %#v", got, want)
	}
}

// TestDelegatedPolicySnapshotEmpty 校验 nil 与空映射都安全，返回非 nil 的
// 快照且只带来源标记。
func TestDelegatedPolicySnapshotEmpty(t *testing.T) {
	for name, parent := range map[string]map[string]string{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			got := delegatedPolicySnapshot(parent)
			if got == nil {
				t.Fatal("返回了 nil 映射")
			}
			want := map[string]string{"source": policySourceDelegated}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("空输入快照 = %#v, want %#v", got, want)
			}
		})
	}
}

// TestDelegatedPolicySnapshotCJK 校验 CJK 值按原样复制：既不丢失也不产生
// 半个 UTF-8 序列。
func TestDelegatedPolicySnapshotCJK(t *testing.T) {
	parent := map[string]string{
		"sandbox":  "只读沙箱",
		"approval": "每次询问操作员",
		"preset":   "中文预设（严格）",
	}

	got := delegatedPolicySnapshot(parent)
	for _, key := range []string{"sandbox", "approval", "preset"} {
		if got[key] != parent[key] {
			t.Errorf("%s 的 CJK 值被改写: got %q, want %q", key, got[key], parent[key])
		}
		if !utf8.ValidString(got[key]) {
			t.Errorf("%s 的 CJK 值不是合法 UTF-8: %q", key, got[key])
		}
	}
	if got["source"] != policySourceDelegated {
		t.Errorf("来源标记 = %q, want %q", got["source"], policySourceDelegated)
	}
}

// TestDelegatedPolicySnapshotDoesNotMutateInput 校验不修改入参：调用前后父映射
// 完全一致，且改写快照不会影响父映射（二者不共享状态）。
func TestDelegatedPolicySnapshotDoesNotMutateInput(t *testing.T) {
	parent := map[string]string{
		"sandbox":  "workspace-write",
		"approval": "ask",
		"preset":   "coding",
		"extra":    "untouched",
	}
	before := make(map[string]string, len(parent))
	for k, v := range parent {
		before[k] = v
	}

	snapshot := delegatedPolicySnapshot(parent)
	if !reflect.DeepEqual(parent, before) {
		t.Fatalf("入参被修改: got %#v, want %#v", parent, before)
	}

	snapshot["sandbox"] = "danger-full-access"
	snapshot["approval"] = "never"
	if parent["sandbox"] != before["sandbox"] || parent["approval"] != before["approval"] {
		t.Fatalf("改写快照影响了入参: %#v", parent)
	}
}

// TestDelegatedPolicySnapshotMarked 校验来源标记始终为 delegation，且父级
// 自带的 source 键不会覆盖它（标记由快照权威给出）。
func TestDelegatedPolicySnapshotMarked(t *testing.T) {
	for _, parentSource := range []string{"", "user-config", "parent", policySourceDelegated} {
		parent := map[string]string{"source": parentSource}
		got := delegatedPolicySnapshot(parent)
		if got["source"] != policySourceDelegated {
			t.Fatalf("parent source=%q 时来源标记 = %q, want %q",
				parentSource, got["source"], policySourceDelegated)
		}
	}
}

// TestDelegatedPolicySnapshotStable 校验稳定性：多次调用结果一致，且与父映射
// 的键插入顺序无关。
func TestDelegatedPolicySnapshotStable(t *testing.T) {
	entries := map[string]string{
		"sandbox":  "read-only",
		"approval": "never",
		"preset":   "review",
	}

	first := delegatedPolicySnapshot(entries)
	for i := 0; i < 16; i++ {
		got := delegatedPolicySnapshot(entries)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次调用结果不一致: got %#v, want %#v", i+1, got, first)
		}
	}

	// 以不同插入顺序重建父映射，快照内容仍应一致。
	orderA := map[string]string{}
	orderA["preset"] = entries["preset"]
	orderA["approval"] = entries["approval"]
	orderA["sandbox"] = entries["sandbox"]
	if got := delegatedPolicySnapshot(orderA); !reflect.DeepEqual(got, first) {
		t.Fatalf("插入顺序影响了快照: got %#v, want %#v", got, first)
	}
}

// TestDelegatedPolicySnapshotExportedMatches 校验导出的接线缝与内部纯函数语义一致。
func TestDelegatedPolicySnapshotExportedMatches(t *testing.T) {
	parent := map[string]string{"sandbox": "workspace-write", "preset": "中文"}
	if got, want := DelegatedPolicySnapshot(parent), delegatedPolicySnapshot(parent); !reflect.DeepEqual(got, want) {
		t.Fatalf("导出封装 = %#v, want %#v", got, want)
	}
}
