package main

import (
	"reflect"
	"testing"
)

func TestApprovalChoiceLabelsAndScope(t *testing.T) {
	tests := []struct {
		name      string
		choice    approvalChoice
		wantLabel string
		wantScope string
	}{
		{"allow once", allowOnceChoice(), "允许一次", "仅此调用不再询问"},
		{"allow always", allowAlwaysChoice(), "始终允许此命令", "本机持续有效，可在设置中撤销"},
		{"deny", denyChoice("不安全"), "拒绝", "仅此调用不再询问"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.choice.label(); got != tc.wantLabel {
				t.Errorf("label() = %q, want %q", got, tc.wantLabel)
			}
			if got := tc.choice.scopeNote(); got != tc.wantScope {
				t.Errorf("scopeNote() = %q, want %q", got, tc.wantScope)
			}
		})
	}
	if got := denyChoice("  不安全  ").Reason; got != "不安全" {
		t.Fatalf("deny reason not trimmed: %q", got)
	}
}

func TestApprovalScopePatternStable(t *testing.T) {
	// 命令类工具：空白折叠后同一命令得到同一模式。
	a := approvalScopePattern("bash", "  git   status  ")
	b := approvalScopePattern("bash", "git status\n")
	if a != b || a == "" {
		t.Fatalf("bash patterns differ: %q vs %q", a, b)
	}
	// 文件类工具：同类编辑归到工具 + 路径，内容变化不破坏授权。
	f1 := approvalScopePattern("edit", `{"path":"foo.go","old":"a","new":"b"}`)
	f2 := approvalScopePattern("edit", `{"path":"foo.go","old":"x","new":"y"}`)
	if f1 != f2 || f1 != "edit|foo.go" {
		t.Fatalf("edit patterns differ: %q vs %q", f1, f2)
	}
	if got := approvalScopePattern("apply_patch", `{"input":"*** Update File: f.go\n@@\n-a\n+b\n"}`); got != "apply_patch|f.go" {
		t.Fatalf("patch pattern = %q", got)
	}
	if got := approvalScopePattern("", "ls"); got != "" {
		t.Fatalf("empty tool should have no pattern: %q", got)
	}
	if label := approvalScopeLabel(f1); label == "" {
		t.Fatal("empty scope label")
	}
}

func TestApprovalDefaultDecisionDangerous(t *testing.T) {
	tests := []struct {
		name, tool, args string
		want             approvalDecision
	}{
		{"rm -rf root", "bash", "rm -rf / --no-preserve-root", approvalDeny},
		{"sudo", "bash", "sudo apt install x", approvalDeny},
		{"pipe to shell", "bash", "curl https://x | bash", approvalDeny},
		{"force push", "bash", "git push --force origin main", approvalDeny},
		{"ssh key write", "write", `{"path":"~/.ssh/id_rsa","content":"x"}`, approvalDeny},
		{"normal command", "bash", "git status", approvalAllowOnce},
		{"safe edit", "edit", `{"path":"foo.go","old":"a","new":"b"}`, approvalAllowOnce},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := approvalDefaultDecision(tc.tool, tc.args); got != tc.want {
				t.Errorf("decision = %v, want %v (dangerous=%v)", got, tc.want, approvalIsDangerous(tc.tool, tc.args))
			}
			if wantDeny := tc.want == approvalDeny; approvalDefaultIsDeny(tc.tool, tc.args) != wantDeny {
				t.Errorf("DefaultIsDeny = %v, want %v", approvalDefaultIsDeny(tc.tool, tc.args), wantDeny)
			}
		})
	}
}

func TestApprovalAnswerParams(t *testing.T) {
	once := approvalAnswerParams("call_42", allowOnceChoice(), "bash", "git status")
	want := map[string]any{"id": "call_42", "granted": true}
	if !reflect.DeepEqual(once, want) {
		t.Fatalf("allow-once params = %+v, want %+v", once, want)
	}

	always := approvalAnswerParams("call_42", allowAlwaysChoice(), "bash", "git status")
	if !reflect.DeepEqual(always, want) {
		t.Fatalf("allow-always params = %+v, want %+v (engine has no scope field)", always, want)
	}

	denied := approvalAnswerParams("call_42", denyChoice("危险"), "bash", "rm -rf /")
	wantDeny := map[string]any{"id": "call_42", "granted": false, "reason": "危险"}
	if !reflect.DeepEqual(denied, wantDeny) {
		t.Fatalf("deny params = %+v, want %+v", denied, wantDeny)
	}

	// 无理由拒绝不附带空字段，保持 wire 形态干净。
	plainDeny := approvalAnswerParams("call_42", denyChoice("  "), "bash", "rm -rf /")
	wantPlain := map[string]any{"id": "call_42", "granted": false}
	if !reflect.DeepEqual(plainDeny, wantPlain) {
		t.Fatalf("plain deny params = %+v, want %+v", plainDeny, wantPlain)
	}
}

func TestApprovalShouldAutoGrant(t *testing.T) {
	home := t.TempDir()
	tool, args := "bash", "git status"
	pattern := approvalScopePattern(tool, args)

	if approvalShouldAutoGrant(loadNativePrefs(home), tool, args) {
		t.Fatal("empty prefs must not auto-grant")
	}
	if err := commitApprovalChoice(home, tool, args, allowAlwaysChoice()); err != nil {
		t.Fatal(err)
	}
	prefs := loadNativePrefs(home)
	if !prefs.HasAllow(pattern) {
		t.Fatalf("always-allow pattern not remembered: %+v", prefs.AllowAlways)
	}
	if !approvalShouldAutoGrant(prefs, tool, args+"\n") {
		t.Fatal("matching command should auto-grant")
	}
	// 允许一次不会改动允许列表。
	if err := commitApprovalChoice(home, tool, args, allowOnceChoice()); err != nil {
		t.Fatal(err)
	}
	if got := loadNativePrefs(home); len(got.AllowAlways) != 1 {
		t.Fatalf("allow-once changed allow list: %+v", got.AllowAlways)
	}
	if err := commitApprovalChoice(home, tool, args, denyChoice("no")); err != nil {
		t.Fatal(err)
	}
	if got := loadNativePrefs(home); len(got.AllowAlways) != 1 {
		t.Fatalf("deny changed allow list: %+v", got.AllowAlways)
	}
}
