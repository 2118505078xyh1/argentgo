package security

import (
	"encoding/json"
	"testing"
)

// TestGlobMatchTrailingSlashMatchesDescendants 验证以 `/` 结尾的模式能匹配该目录本身
// 及其下任意深度的路径。修复前 "src/" 会在比较时再拼出一个 "/"，导致 "src/file"
// 永远匹配不上，使 denyPaths 中以斜杠结尾的规则形同虚设。
func TestGlobMatchTrailingSlashMatchesDescendants(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"字面量目录匹配直接子文件", "src/", "src/main.go", true},
		{"字面量目录匹配深层文件", "src/", "src/a/b/c.rs", true},
		{"字面量目录匹配目录本身", "src/", "src", true},
		{"字面量目录出现在路径中段", "src/", "project/src/main.go", true},
		{"前置斜杠不影响匹配", "/src/", "src/main.go", true},
		{"反斜杠结尾按目录处理", `src\`, "src/main.go", true},
		{"通配符模式匹配目录内文件", "**/.git/", "repo/.git/config", true},
		{"通配符模式匹配深层文件", "**/.git/", "repo/.git/hooks/pre-commit", true},
		{"不误匹配同名前缀目录", "src/", "srcx/main.go", false},
		{"不误匹配无关路径", "src/", "docs/readme.md", false},
		{"普通前缀语义保持不变", "src", "src/main.go", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GlobMatch(tc.pattern, tc.path); got != tc.want {
				t.Fatalf("GlobMatch(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// TestPolicyDenyPathTrailingSlash 验证策略层：以 `/` 结尾的 denyPaths 必须真正拦截
// 该目录下的读写，而不是让规则静默失效。
func TestPolicyDenyPathTrailingSlash(t *testing.T) {
	root := t.TempDir()
	policy, err := PolicyFromJSON(root, []byte(`{"denyPaths":["secret/"]}`))
	if err != nil {
		t.Fatalf("PolicyFromJSON: %v", err)
	}
	for _, tc := range []struct {
		path string
		want Decision
	}{
		{"secret/id_rsa", Deny},
		{"secret/nested/key.pem", Deny},
		{"src/main.go", Allow}, // 无关路径不受影响
	} {
		got := policy.Check("read", json.RawMessage(mustJSON(t, map[string]string{"path": tc.path})))
		if got != tc.want {
			t.Fatalf("Check(read, path=%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// mustJSON 把值编码为 JSON 字节，失败时让测试立即终止。
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

// TestGlobMatchTrailingSlashDoesNotMatchEmpty 防止把目录模式误改成匹配空路径。
func TestGlobMatchTrailingSlashDoesNotMatchEmpty(t *testing.T) {
	if GlobMatch("src/", "") {
		t.Fatal("a directory pattern must not match an empty path")
	}
}
