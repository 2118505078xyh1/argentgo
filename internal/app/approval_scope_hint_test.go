package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type grantingApprover struct{ granted bool }

func (g grantingApprover) Ask(_ context.Context, _ string, _ string) (bool, error) {
	return g.granted, nil
}

// TestApprovalScopeContextHint 锁定 #25：已批准作用域进入有界 system 提示，
// 自由文本参数不出现；作用域来自持久化的 approvals.jsonl，因此压缩后重建 system
// 仍保留；=0 回退逐字节不变。
func TestApprovalScopeContextHint(t *testing.T) {
	storage := t.TempDir()
	line := `{"v":1,"kind":"always","scope":"git status","ts":1}` + "\n" +
		`{"v":1,"kind":"always","scope":"edit","ts":2}` + "\n" +
		`{"v":1,"kind":"deny","scope":"bash: rm","ts":3}` + "\n" // 粘性 deny 不进提示
	if err := os.WriteFile(filepath.Join(storage, "approvals.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	const freeText = "请把 SECRET-ARG 写到 ./somewhere 并回复 detailed free form explanation"
	hint := appendApprovalScopeHint("base system", storage, nil)
	if !strings.HasPrefix(hint, "base system\n") {
		t.Fatalf("system 提示未追加片段: %q", hint)
	}
	for _, want := range []string{"git status", "edit"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("提示缺少作用域 %q: %q", want, hint)
		}
	}
	if strings.Contains(hint, "bash: rm") || strings.Contains(hint, freeText) || strings.Contains(hint, "SECRET-ARG") {
		t.Fatalf("提示泄漏了不该出现的内容: %q", hint)
	}
	if len(hint) > len("base system\n")+approvalScopeHintBudget {
		t.Fatalf("提示片段超过 %d 字节: %d", approvalScopeHintBudget, len(hint))
	}

	// 压缩保留：重建 system 时拿到同一份作用域（压缩只折叠会话消息）。
	again := appendApprovalScopeHint("base system", storage, nil)
	if again != hint {
		t.Fatalf("重建后的提示漂移: %q != %q", again, hint)
	}

	// =0 回退逐字节不变。
	if got := appendApprovalScopeHint("base system", storage, envFrom(map[string]string{approvalScopeHintEnv: "0"})); got != "base system" {
		t.Fatalf("回退路径改变了 system: %q", got)
	}
}

func TestApprovalScopeFromArgsNeverContainsFreeText(t *testing.T) {
	args := `{"command":"git status --porcelain v1 'free form text'","reason":"user asked nicely","stdin":"SECRET-VALUE"}`
	scope := approvalScopeFor("bash", args)
	if scope != "bash: git status" {
		t.Fatalf("scope = %q, want %q", scope, "bash: git status")
	}
	for _, leak := range []string{"free form text", "user asked nicely", "SECRET-VALUE", "--porcelain"} {
		if strings.Contains(scope, leak) {
			t.Fatalf("作用域包含自由文本 %q: %q", leak, scope)
		}
	}
	// 无法解析的命令回落到工具名，绝不把整条 args 当作用域。
	if got := approvalScopeFor("bash", `{`); got != "bash" {
		t.Fatalf("坏参数作用域 = %q, want bash", got)
	}
	if got := approvalScopeFor("edit", args); got != "edit" {
		t.Fatalf("非 shell 工具作用域 = %q, want edit", got)
	}
}

func TestApprovalGrantObserverOnlyRecordsGrants(t *testing.T) {
	hint := &approvalScopeHint{}
	wrapped := observeApprovalGrants(grantingApprover{granted: true}, hint)
	granted, err := wrapped.Ask(context.Background(), "bash", `{"command":"npm install"}`)
	if !granted || err != nil {
		t.Fatalf("决定被改变: %v %v", granted, err)
	}
	if got := hint.render(approvalScopeHintBudget); !strings.Contains(got, "npm install") {
		t.Fatalf("未记录授予作用域: %q", got)
	}

	denied := &approvalScopeHint{}
	wrapped = observeApprovalGrants(grantingApprover{granted: false}, denied)
	_, _ = wrapped.Ask(context.Background(), "bash", `{"command":"rm -rf /"}`)
	if got := denied.render(approvalScopeHintBudget); got != "" {
		t.Fatalf("拒绝不应被记录: %q", got)
	}

	// nil 安全。
	if got := observeApprovalGrants(nil, hint); got != nil {
		t.Fatalf("nil inner 应返回 nil, got %v", got)
	}
}
