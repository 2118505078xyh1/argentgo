package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestDenyPathsApplyToEveryPathArgumentName 守护一次真实策略绕过漏洞的修复：
// pathValues 过去只识别 "path"/"file"/"dir"/"directory"/"target"，而内置 diff
// 工具把它操作数写作 "path_a" 和 "path_b"。因此 deny-path 规则在 diff 调用中
// 根本看不到路径，让被拒绝的文件得以被读取（Ask 会落到自动批准）。
func TestDenyPathsApplyToEveryPathArgumentName(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{DenyPaths: []string{"**/.env"}})

	for _, key := range pathArgKeys {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:".env"}`, key))
			got := policy.Evaluate("read", args)
			if got.Decision != Deny {
				t.Fatalf("read with %s=.env: decision = %v (%q), want Deny", key, got.Decision, got.Reason)
			}
		})
	}

	// 名字不表示路径的字段必须留在路径规则之外，否则该修复会变成过于宽泛的
	// 字符串扫描。
	t.Run("non path field untouched", func(t *testing.T) {
		args := json.RawMessage(`{"note":".env"}`)
		if got := policy.Evaluate("read", args); got.Decision != Allow {
			t.Fatalf("read with a non-path field: decision = %v (%q), want Allow", got.Decision, got.Reason)
		}
	})
}

// TestDenyPathsApplyToEveryPathArray 覆盖数组形式：checkpoint 恢复清单中的
// "paths" 和 context_pack 中的 "files"。两者都会读取文件内容，因此 deny-path
// 规则也必须应用到它们的元素上。
func TestDenyPathsApplyToEveryPathArray(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{DenyPaths: []string{"**/.env"}})
	for _, key := range pathArrayKeys {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:["main.go",".env"]}`, key))
			got := policy.Evaluate("read", args)
			if got.Decision != Deny {
				t.Fatalf("read with %s containing .env: decision = %v (%q), want Deny", key, got.Decision, got.Reason)
			}
		})
	}
}

// TestRuleDecisionCoversAlternatePathKeys 证明权限表与 deny-path 关卡看到相同的
// 资源，包括新键名下的数组条目。表中任意位置的 deny 行都是绝对的。
func TestRuleDecisionCoversAlternatePathKeys(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{Rules: []Rule{
		{Decision: Ask, Tool: "read", Path: "**/notes.txt"},
		{Decision: Deny, Tool: "read", Path: "**/secret.txt"},
	}})

	cases := []struct {
		name string
		args string
		want Decision
	}{
		{"scalar path_a", `{"path_a":"notes.txt","path_b":"main.go"}`, Ask},
		{"scalar file_path", `{"file_path":"notes.txt"}`, Ask},
		{"array files", `{"target":"main.go","files":["notes.txt"]}`, Ask},
		{"array paths", `{"paths":["main.go","notes.txt"]}`, Ask},
		// 数组条目上的 deny 必须胜过标量上的 Ask：这正是遗漏某个键过去会破坏的
		// 取最严格判定的遍历。
		{"deny in path_b", `{"path_a":"notes.txt","path_b":"secret.txt"}`, Deny},
		{"deny in files", `{"files":["notes.txt","secret.txt"]}`, Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.Evaluate("read", json.RawMessage(tc.args))
			if got.Decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v (%q), want %v", tc.args, got.Decision, got.Reason, tc.want)
			}
		})
	}
}

// TestWriteRootsCoverAlternatePathKeys 验证修改类工具不能仅通过给路径参数换个
// 名字就逃出写入边界。
func TestWriteRootsCoverAlternatePathKeys(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "escape.txt")
	policy := NewPolicy(root, PolicyConfig{})

	for _, key := range []string{"path_a", "file_path", "old_path"} {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:%q}`, key, filepath.ToSlash(target)))
			got := policy.Evaluate("write", args)
			if got.Decision != Deny {
				t.Fatalf("write with %s outside the root: decision = %v (%q), want Deny", key, got.Decision, got.Reason)
			}
		})
	}
}

// TestApprovalArgsRewritesAlternatePathKeys 保证提示显示诚实：备用路径键下的链接
// 必须显示在其真实位置，而不是工具收到的无害词法写法。
func TestApprovalArgsRewritesAlternatePathKeys(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	makeDirectoryLink(t, outside, alias)

	want, err := ResolveRealPath(filepath.Join(root, "alias", "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"path_a", "file_path"} {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:"alias/note.txt"}`, key))
			var shown map[string]string
			if err := json.Unmarshal(approvalArgs(args, root), &shown); err != nil {
				t.Fatal(err)
			}
			if shown[key] != want {
				t.Fatalf("approvalArgs(%s)[%s] = %q, want the real path %q", args, key, shown[key], want)
			}
		})
	}
}
