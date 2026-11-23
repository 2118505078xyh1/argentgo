package security

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestBashHierarchicalScopes 验证 #27：bash/powershell 的 always 作用域按
// token 数生成分层通配前缀并去重；持久化作用域不写自由文本参数；分层授权
// 让同前缀的后续命令不再重复审批。
func TestBashHierarchicalScopes(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args string
		want []string
	}{
		{"three tokens", "bash", `{"command":"npm install lodash"}`, []string{"npm *", "npm install *", "npm install lodash"}},
		{"two tokens", "bash", `{"command":"git status"}`, []string{"git *", "git status"}},
		{"single token", "bash", `{"command":"make"}`, []string{"make"}},
		{"arity prefix dedupe", "bash", `{"command":"npm install *"}`, []string{"npm *", "npm install *"}},
		{"script alias", "powershell", `{"script":"Get-ChildItem C:\\temp"}`, []string{"Get-ChildItem *", `Get-ChildItem C:\temp`}},
		{"non shell tool", "read", `{"path":"a.txt"}`, nil},
		{"empty command", "bash", `{"command":"   "}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BashScopeFor(tt.tool, tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("BashScopeFor(%s, %s) = %#v, want %#v", tt.tool, tt.args, got, tt.want)
			}
		})
	}

	// 端到端：批准 npm install lodash 后，分层前缀一并落盘；换用另一个 lodash
	// 版本时仍命中 "npm install *"，无需重新审批。
	dir := t.TempDir()
	first := NewFileBackedScopedApproverHierarchical(humanAlwaysApprover{}, dir, BashScopeFor)
	if ok, err := first.Ask(context.Background(), "bash", `{"command":"npm install lodash"}`); err != nil || !ok {
		t.Fatalf("first bash approval: ok=%v err=%v", ok, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, persistentApprovalsFile))
	if err != nil {
		t.Fatalf("read approvals file: %v", err)
	}
	for _, scope := range []string{`npm *`, `npm install *`, `npm install lodash`} {
		if !bytes.Contains(data, []byte(`"scope":"`+scope+`"`)) {
			t.Fatalf("approvals file missing scope %q: %s", scope, data)
		}
	}
	if bytes.Contains(data, []byte("lodash@")) {
		t.Fatalf("approvals file must not store free-text args: %s", data)
	}

	second := NewFileBackedScopedApproverHierarchical(NeverApprover{}, dir, BashScopeFor)
	if ok, err := second.Ask(context.Background(), "bash", `{"command":"npm install foo"}`); err != nil || !ok {
		t.Fatalf("hierarchical prefix should auto-allow npm install foo: ok=%v err=%v", ok, err)
	}
	if ok, err := second.Ask(context.Background(), "bash", `{"command":"git status"}`); err != nil || ok {
		t.Fatalf("unrelated command family must still require approval: ok=%v err=%v", ok, err)
	}
}
