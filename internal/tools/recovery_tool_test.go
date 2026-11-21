package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// recoveryRunArgs 把路径与行范围编码成 Run 接受的 JSON 参数。
func recoveryRunArgs(t *testing.T, path string, start, end int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"path":       path,
		"start_line": start,
		"end_line":   end,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// recoverySpecNames 提取 spec 名称。specNames 位于外部测试包
// tools_test 中，本文件是内部测试包，因此本地复刻该辅助函数。
func recoverySpecNames(specs []llm.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Function.Name)
	}
	return names
}

// TestRecoveryToolInterface 表驱动校验 Tool 接口的每个方法与 schema 结构。
func TestRecoveryToolInterface(t *testing.T) {
	tool := NewRecoveryTool(t.TempDir())

	if got := tool.Name(); got != "read_output_artifact" {
		t.Fatalf("Name() = %q, want %q", got, "read_output_artifact")
	}
	if tool.Mutating() {
		t.Fatal("Mutating() = true, want false (recovery must be read-only)")
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Fatal("Description() is empty")
	}

	schema := tool.Schema()
	if !json.Valid([]byte(schema)) {
		t.Fatalf("Schema() is not valid JSON: %s", schema)
	}
	var doc struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Type != "object" {
		t.Fatalf("schema type = %q, want object", doc.Type)
	}
	for _, field := range []string{"path", "start_line", "end_line"} {
		if _, ok := doc.Properties[field]; !ok {
			t.Fatalf("schema missing property %q", field)
		}
	}
	if doc.Properties["path"].Type != "string" || doc.Properties["start_line"].Type != "integer" || doc.Properties["end_line"].Type != "integer" {
		t.Fatalf("schema property types wrong: %+v", doc.Properties)
	}
	if !reflect.DeepEqual(doc.Required, []string{"path"}) {
		t.Fatalf("schema required = %v, want [path]", doc.Required)
	}
}

// TestRecoveryToolRunReadsLineRanges 表驱动校验合法读取与行范围语义
// （语义由 SpillRecovery 保证，这里确认 Run 的参数接线正确）。
func TestRecoveryToolRunReadsLineRanges(t *testing.T) {
	dir := t.TempDir()
	path := writeRecoveryArtifact(t, dir, "l1\nl2\nl3\nl4\nl5\n")
	tool := NewRecoveryTool(dir)

	for _, tc := range []struct {
		name       string
		start, end int
		want       string
	}{
		{"middle inclusive range", 2, 4, "l2\nl3\nl4"},
		{"single line", 3, 3, "l3"},
		{"start clamped up to one", 0, 2, "l1\nl2"},
		{"negative start clamped", -5, 1, "l1"},
		{"end clamped to total", 4, 999, "l4\nl5"},
		{"to EOF keeps trailing newline", 2, 0, "l2\nl3\nl4\nl5\n"},
		{"start past total is empty", 6, 10, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tool.Run(context.Background(), t.TempDir(), recoveryRunArgs(t, path, tc.start, tc.end))
			if err != nil {
				t.Fatalf("Run(%d, %d): %v", tc.start, tc.end, err)
			}
			if got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRecoveryToolRunRejectsUnsafePaths 表驱动校验路径遍历、非产出文件名、
// 目录、符号链接与空路径都被拒绝（校验由 SpillRecovery 保证）。
func TestRecoveryToolRunRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	writeRecoveryArtifact(t, dir, "legitimate\n")

	sep := string(filepath.Separator)
	traversal := dir + sep + ".." + sep + filepath.Base(dir) + sep + "tool-output-0123456789abcdef.log"
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "tool-output-fedcba9876543210.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	missing := filepath.Join(dir, "tool-output-0000000000000000.log")

	tool := NewRecoveryTool(dir)
	for _, tc := range []struct {
		name    string
		path    string
		wantErr error
	}{
		{"traversal segment", traversal, errSpillInvalidPath},
		{"non artifact filename", notes, errSpillNotArtifact},
		{"directory as artifact", dir, errSpillNotArtifact},
		{"symlink artifact", link, errSpillInvalidPath},
		{"missing artifact", missing, nil},
		{"empty path", "", errSpillInvalidPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tool.Run(context.Background(), dir, recoveryRunArgs(t, tc.path, 1, 1))
			if err == nil {
				t.Fatalf("Run(%q) unexpectedly succeeded with %q", tc.path, got)
			}
			if strings.Contains(got, "secret") {
				t.Fatalf("Run(%q) leaked file content: %q", tc.path, got)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tc.wantErr)
			}
		})
	}
}

// TestRecoveryToolRunBadArguments 校验损坏的 JSON 参数以工具错误返回。
func TestRecoveryToolRunBadArguments(t *testing.T) {
	tool := NewRecoveryTool(t.TempDir())
	for _, raw := range []string{"", "not json", `{"path":`, `{"path": 42}`} {
		if _, err := tool.Run(context.Background(), "", json.RawMessage(raw)); err == nil {
			t.Fatalf("Run(%q) unexpectedly succeeded", raw)
		}
	}
}

// TestRecoveryToolCJK 校验含中文与 emoji 的产出经 Run 取回后仍是有效 UTF-8。
func TestRecoveryToolCJK(t *testing.T) {
	dir := t.TempDir()
	content := "中文😀第一行\nsecond 世界🌍line\n再见👋third\n末行 End 行\n"
	path := writeRecoveryArtifact(t, dir, content)
	tool := NewRecoveryTool(dir)

	got, err := tool.Run(context.Background(), dir, recoveryRunArgs(t, path, 2, 3))
	if err != nil {
		t.Fatal(err)
	}
	want := "second 世界🌍line\n再见👋third"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("range contains broken runes: %q", got)
	}

	full, err := tool.Run(context.Background(), dir, recoveryRunArgs(t, path, 1, 0))
	if err != nil || full != content {
		t.Fatalf("full read = (%q, %v), want (%q, nil)", full, err, content)
	}
}

// recoveryGoldenNames 是默认 New 注册表的 golden 顺序，与
// builtin_register_test.go 的锁定清单一致；恢复工具不得出现在其中。
var recoveryGoldenNames = []string{
	"read", "write", "edit", "bash", "ls", "grep", "find", "powershell",
	"glob", "todo", "multi_edit", "web_fetch", "git_status", "current_time",
	"web_search", "repo_map", "submit_plan",
	"lsp_symbols", "lsp_references", "lsp_code_actions", "lsp_rename_preview",
	"lsp_format", "lsp_workspace_symbols", "security_scan",
	"diff", "usage", "history", "context_pack",
	"memory_retain", "memory_recall", "memory_forget", "memory_reflect",
	"memory_learn", "tool_search",
}

// TestRegisterRecoveryOptIn 校验 opt-in 注册：默认 golden 不变，
// 显式注册后仅尾部新增、顺序稳定、幂等且可由 Registry.Run 调用。
func TestRegisterRecoveryOptIn(t *testing.T) {
	r := New(t.TempDir(), true)

	before := recoverySpecNames(r.Specs())
	if !reflect.DeepEqual(before, recoveryGoldenNames) {
		t.Fatalf("default Specs() order changed:\ngot  = %v\nwant = %v", before, recoveryGoldenNames)
	}
	for _, name := range before {
		if name == "read_output_artifact" {
			t.Fatal("read_output_artifact leaked into the default registry")
		}
	}

	dir := t.TempDir()
	path := writeRecoveryArtifact(t, dir, "alpha\nbeta\ngamma\n")
	RegisterRecovery(r, dir)

	after := recoverySpecNames(r.Specs())
	wantAfter := append(append([]string{}, recoveryGoldenNames...), "read_output_artifact")
	if !reflect.DeepEqual(after, wantAfter) {
		t.Fatalf("Specs() order after register:\ngot  = %v\nwant = %v", after, wantAfter)
	}
	if !reflect.DeepEqual(r.Names(), after) {
		t.Fatalf("Names() = %v, want %v", r.Names(), after)
	}

	// 幂等：重复注册不改变顺序。
	RegisterRecovery(r, dir)
	if got := recoverySpecNames(r.Specs()); !reflect.DeepEqual(got, wantAfter) {
		t.Fatalf("second RegisterRecovery changed order: %v", got)
	}

	// nil 注册表是空操作。
	RegisterRecovery(nil, dir)

	// 经 Registry.Run 实际读取，确认接线后的参数解析正确。
	out, err := r.Run(context.Background(), "read_output_artifact", string(recoveryRunArgs(t, path, 2, 3)))
	if err != nil {
		t.Fatalf("Registry.Run: %v", err)
	}
	if out != "beta\ngamma" {
		t.Fatalf("Registry.Run output = %q, want %q", out, "beta\ngamma")
	}
}

// TestRegisterRecoveryIndependentOfOtherRegistries 校验注册只影响被传入的
// 注册表；未显式注册的另一份注册表仍保持 golden。
func TestRegisterRecoveryIndependentOfOtherRegistries(t *testing.T) {
	a := New(t.TempDir(), true)
	b := New(t.TempDir(), true)
	RegisterRecovery(a, t.TempDir())

	if got := recoverySpecNames(b.Specs()); !reflect.DeepEqual(got, recoveryGoldenNames) {
		t.Fatalf("unregistered registry Specs() changed: %v", got)
	}
	if got := recoverySpecNames(a.Specs()); len(got) != len(recoveryGoldenNames)+1 {
		t.Fatalf("registered registry Specs() length = %d, want %d", len(got), len(recoveryGoldenNames)+1)
	}
}
