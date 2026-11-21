package agent

import (
	"reflect"
	"sort"
	"testing"
)

// namesContain 报告 names 是否包含 want。
func namesContain(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// TestDeriveChildPolicyInheritsParentDeny 验证子级继承父级的所有拒绝项。
func TestDeriveChildPolicyInheritsParentDeny(t *testing.T) {
	parent := DenySet{"bash": {}, "write": {}}
	got := DeriveChildPolicy(parent, SpawnPolicy{})

	want := []string{"bash", "spawn", "task", "todowrite", "write"}
	if !reflect.DeepEqual(got.Deny, want) {
		t.Errorf("Deny = %#v, want %#v", got.Deny, want)
	}
	if got.Allow != nil {
		t.Errorf("Allow = %#v, want nil", got.Allow)
	}
}

// TestDeriveChildPolicyDefaultDeniesTaskAndTodoWrite 验证默认强制拒绝 task/todowrite/spawn。
func TestDeriveChildPolicyDefaultDeniesTaskAndTodoWrite(t *testing.T) {
	got := DeriveChildPolicy(nil, SpawnPolicy{})

	want := []string{"spawn", "task", "todowrite"}
	if !reflect.DeepEqual(got.Deny, want) {
		t.Errorf("Deny = %#v, want %#v", got.Deny, want)
	}
	for _, name := range []string{"task", "todowrite", "spawn"} {
		if !namesContain(got.Deny, name) {
			t.Errorf("Deny 缺少默认拒绝项 %q: %#v", name, got.Deny)
		}
	}
}

// TestDeriveChildPolicyExplicitAllowExemptsTask 验证显式 ChildAllow 可豁免 task。
func TestDeriveChildPolicyExplicitAllowExemptsTask(t *testing.T) {
	got := DeriveChildPolicy(nil, SpawnPolicy{ChildAllow: []string{"task"}})

	if !reflect.DeepEqual(got.Allow, []string{"task"}) {
		t.Errorf("Allow = %#v, want [task]", got.Allow)
	}
	wantDeny := []string{"spawn", "todowrite"}
	if !reflect.DeepEqual(got.Deny, wantDeny) {
		t.Errorf("Deny = %#v, want %#v", got.Deny, wantDeny)
	}
	if namesContain(got.Deny, "task") {
		t.Errorf("task 已被豁免，不应出现在 Deny: %#v", got.Deny)
	}
}

// TestDeriveChildPolicySpawnNeverExempt 验证 spawn 永不豁免，即使被显式列出。
func TestDeriveChildPolicySpawnNeverExempt(t *testing.T) {
	got := DeriveChildPolicy(nil, SpawnPolicy{ChildAllow: []string{"task", "todowrite", "spawn"}})

	if !namesContain(got.Deny, "spawn") {
		t.Errorf("spawn 必须永远在 Deny 中: %#v", got.Deny)
	}
	if namesContain(got.Allow, "spawn") {
		t.Errorf("spawn 不应出现在 Allow: %#v", got.Allow)
	}
	wantAllow := []string{"task", "todowrite"}
	if !reflect.DeepEqual(got.Allow, wantAllow) {
		t.Errorf("Allow = %#v, want %#v", got.Allow, wantAllow)
	}
}

// TestDeriveChildPolicyParentDenyCannotBeExempted 验证父级拒绝不可被子级豁免。
func TestDeriveChildPolicyParentDenyCannotBeExempted(t *testing.T) {
	parent := DenySet{"task": {}, "bash": {}}
	got := DeriveChildPolicy(parent, SpawnPolicy{ChildAllow: []string{"task", "bash"}})

	if got.Allow != nil {
		t.Errorf("父级显式拒绝的项不应被豁免，Allow = %#v", got.Allow)
	}
	for _, name := range []string{"task", "bash"} {
		if !namesContain(got.Deny, name) {
			t.Errorf("父级拒绝项 %q 丢失: %#v", name, got.Deny)
		}
	}
}

// TestDeriveChildPolicyDenyToolsCannotBeExempted 验证 spawn 追加的拒绝不可被豁免。
func TestDeriveChildPolicyDenyToolsCannotBeExempted(t *testing.T) {
	got := DeriveChildPolicy(nil, SpawnPolicy{
		DenyTools:  []string{"grep"},
		ChildAllow: []string{"grep", "task"},
	})

	if namesContain(got.Allow, "grep") {
		t.Errorf("DenyTools 中的 grep 不应被豁免: %#v", got.Allow)
	}
	if !namesContain(got.Deny, "grep") {
		t.Errorf("DenyTools 中的 grep 应保留在 Deny: %#v", got.Deny)
	}
	if !reflect.DeepEqual(got.Allow, []string{"task"}) {
		t.Errorf("Allow = %#v, want [task]", got.Allow)
	}
}

// TestDeriveChildPolicyStableSortedOutput 验证输出去重且排序稳定、可重复。
func TestDeriveChildPolicyStableSortedOutput(t *testing.T) {
	parent := DenySet{"写入": {}, "读取": {}, "bash": {}}
	child := SpawnPolicy{
		DenyTools:  []string{"编辑", "write"},
		ChildAllow: []string{"task", "task"},
	}

	first := DeriveChildPolicy(parent, child)
	if !sort.StringsAreSorted(first.Deny) || !sort.StringsAreSorted(first.Allow) {
		t.Errorf("输出未按字典序排序: Deny=%#v Allow=%#v", first.Deny, first.Allow)
	}

	// 多次调用结果必须完全一致（map 遍历顺序不得泄漏到输出）。
	for i := 0; i < 20; i++ {
		again := DeriveChildPolicy(parent, child)
		if !reflect.DeepEqual(again, first) {
			t.Fatalf("第 %d 次调用结果不一致: got %#v, want %#v", i, again, first)
		}
	}

	// CJK 工具名必须完整保留，不得被按字节截断。
	for _, name := range []string{"写入", "读取", "编辑"} {
		if !namesContain(first.Deny, name) {
			t.Errorf("CJK 工具名 %q 未完整保留: %#v", name, first.Deny)
		}
	}
}

// TestDeriveChildPolicyDoesNotMutateInputs 验证函数不修改任何入参。
func TestDeriveChildPolicyDoesNotMutateInputs(t *testing.T) {
	parent := DenySet{"bash": {}, "task": {}}
	child := SpawnPolicy{
		DenyTools:  []string{"write"},
		ChildAllow: []string{"task", "todowrite"},
	}

	parentBefore := make(DenySet, len(parent))
	for k, v := range parent {
		parentBefore[k] = v
	}
	denyToolsBefore := append([]string(nil), child.DenyTools...)
	childAllowBefore := append([]string(nil), child.ChildAllow...)

	DeriveChildPolicy(parent, child)

	if !reflect.DeepEqual(parent, parentBefore) {
		t.Errorf("parent 被修改: got %#v, want %#v", parent, parentBefore)
	}
	if !reflect.DeepEqual(child.DenyTools, denyToolsBefore) {
		t.Errorf("child.DenyTools 被修改: got %#v, want %#v", child.DenyTools, denyToolsBefore)
	}
	if !reflect.DeepEqual(child.ChildAllow, childAllowBefore) {
		t.Errorf("child.ChildAllow 被修改: got %#v, want %#v", child.ChildAllow, childAllowBefore)
	}
}
