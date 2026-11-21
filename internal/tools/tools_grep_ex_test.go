package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// runGrepEx 直接以 root 为工作区根执行一次 grep_ex。
func runGrepEx(t *testing.T, root, args string) (string, error) {
	t.Helper()
	return grepExTool{}.Run(context.Background(), root, json.RawMessage(args))
}

func TestGrepExToolBasicMatch(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"a.txt": "hello world\nsecond hello\nno match\n",
	})
	out, err := runGrepEx(t, dir, `{"pattern":"hello","case_sensitive":true}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "a.txt:1: hello world\na.txt:2: second hello"
	if out != want {
		t.Fatalf("输出 =\n%s\n期望：\n%s", out, want)
	}
}

func TestGrepExToolOutputModes(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"a.txt": "hit one\nmiss\nhit two\n",
		"b.txt": "hit three\n",
	})

	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{
			name: "content",
			args: `{"pattern":"hit","case_sensitive":true,"output_mode":"content"}`,
			want: "a.txt:1: hit one\na.txt:3: hit two\nb.txt:1: hit three",
		},
		{
			name: "files_with_matches",
			args: `{"pattern":"hit","case_sensitive":true,"output_mode":"files_with_matches"}`,
			want: "a.txt\nb.txt",
		},
		{
			name: "count",
			args: `{"pattern":"hit","case_sensitive":true,"output_mode":"count"}`,
			want: "a.txt:2\nb.txt:1",
		},
		{
			name: "unknown defaults to content",
			args: `{"pattern":"hit","case_sensitive":true,"output_mode":"weird"}`,
			want: "a.txt:1: hit one\na.txt:3: hit two\nb.txt:1: hit three",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runGrepEx(t, dir, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if out != tc.want {
				t.Fatalf("输出 =\n%s\n期望：\n%s", out, tc.want)
			}
		})
	}
}

func TestGrepExToolContextLines(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"a.txt": "alpha\nbeta\ngamma\ndelta\nepsilon\n",
	})

	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{
			name: "context one each side",
			args: `{"pattern":"gamma","case_sensitive":true,"context":1}`,
			want: "a.txt-2- beta\na.txt:3: gamma\na.txt-4- delta",
		},
		{
			name: "before overrides context",
			args: `{"pattern":"gamma","case_sensitive":true,"before":2,"after":1}`,
			want: "a.txt-1- alpha\na.txt-2- beta\na.txt:3: gamma\na.txt-4- delta",
		},
		{
			name: "after only",
			args: `{"pattern":"gamma","case_sensitive":true,"after":2}`,
			want: "a.txt:3: gamma\na.txt-4- delta\na.txt-5- epsilon",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runGrepEx(t, dir, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if out != tc.want {
				t.Fatalf("输出 =\n%s\n期望：\n%s", out, tc.want)
			}
		})
	}
}

func TestGrepExToolIncludeAndCase(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{
		"a.go":     "Hello world\n",
		"b.txt":    "Hello there\n",
		"sub/c.go": "hello again\n",
	})

	// include glob 只保留 *.go。
	out, err := runGrepEx(t, dir, `{"pattern":"hello","include":"*.go","case_sensitive":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a.go:1: Hello world\nsub/c.go:1: hello again"; out != want {
		t.Fatalf("include 输出 =\n%s\n期望：\n%s", out, want)
	}

	// 大小写敏感时不命中。
	out, err = runGrepEx(t, dir, `{"pattern":"hello","include":"b.txt","case_sensitive":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no matches)" {
		t.Fatalf("大小写敏感不应命中，实际 %q", out)
	}

	// 大小写不敏感时命中。
	out, err = runGrepEx(t, dir, `{"pattern":"HELLO","include":"b.txt","case_sensitive":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "b.txt:1: Hello there" {
		t.Fatalf("大小写不敏感输出 = %q", out)
	}
}

func TestGrepExToolEmptyResultAndUseless(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{"a.txt": "nothing relevant\n"})
	out, err := runGrepEx(t, dir, `{"pattern":"absent-9f3a","case_sensitive":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no matches)" {
		t.Fatalf("空结果 = %q", out)
	}

	tool := grepExTool{}
	meta := tool.Meta(nil, out, nil)
	if !meta.Useless {
		t.Fatal("空结果应标记 Useless")
	}
	if got := tool.Meta(nil, "a.txt:1: hit", nil); got.Useless {
		t.Fatal("非空结果不应标记 Useless")
	}
	if got := tool.Meta(nil, out, errSentinel{}); got != (ToolMeta{}) {
		t.Fatalf("出错时应返回零值 ToolMeta，实际 %+v", got)
	}
}

// errSentinel 仅为 Meta 提供一个非 nil 错误。
type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }

func TestGrepExToolPathEscapeRejected(t *testing.T) {
	dir := writeGrepExFiles(t, map[string]string{"a.txt": "hit\n"})
	outside := filepath.Join(filepath.Dir(dir), "outside-grep-ex")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runGrepEx(t, dir, `{"pattern":"hit","path":"../outside-grep-ex"}`)
	if err == nil {
		t.Fatal("逃逸路径应被拒绝")
	}
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("错误应指出越界，实际 %v", err)
	}
}

func TestGrepExToolCJKClipStaysValid(t *testing.T) {
	var content strings.Builder
	for i := 0; i < 200; i++ {
		content.WriteString("命中行 中文内容 alpha beta\n")
	}
	dir := writeGrepExFiles(t, map[string]string{"zh.txt": content.String()})

	ctx := context.WithValue(context.Background(), outputLimitKey{}, 120)
	out, err := grepExTool{}.Run(ctx, dir, json.RawMessage(`{"pattern":"中文","case_sensitive":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(out) {
		t.Fatal("截断后的输出不是合法 UTF-8")
	}
	if !strings.Contains(out, "[truncated]") {
		t.Fatalf("超出上限应被截断，实际 %q", out)
	}
	if strings.ContainsRune(out, utf8.RuneError) {
		t.Fatal("截断不应产生残缺 rune")
	}
}

func TestRegisterGrepExTailAndIdempotent(t *testing.T) {
	r := New(t.TempDir(), true)

	// 默认注册表不含 grep_ex（golden 不变）。
	for _, name := range r.Names() {
		if name == "grep_ex" {
			t.Fatal("默认注册表不应包含 grep_ex")
		}
	}
	before := r.Names()

	RegisterGrepEx(r)
	once := r.Names()
	if len(once) != len(before)+1 {
		t.Fatalf("注册后数量 = %d，期望 %d", len(once), len(before)+1)
	}
	if once[len(once)-1] != "grep_ex" {
		t.Fatalf("grep_ex 应追加在末尾，实际末尾 %q", once[len(once)-1])
	}
	for i := range before {
		if once[i] != before[i] {
			t.Fatalf("既有顺序被改变：第 %d 项 %q != %q", i, once[i], before[i])
		}
	}

	// 幂等：重复注册不再插入。
	RegisterGrepEx(r)
	twice := r.Names()
	if len(twice) != len(once) {
		t.Fatalf("重复注册后数量 = %d，期望 %d", len(twice), len(once))
	}

	// nil 是空操作。
	RegisterGrepEx(nil)

	// Specs 也应包含该工具，且 schema 带意图字段（Specs 兜底注入）。
	found := false
	for _, spec := range r.Specs() {
		if spec.Function.Name == "grep_ex" {
			found = true
			if !strings.Contains(string(spec.Function.Parameters), `"i"`) {
				t.Fatal("Specs 中的 grep_ex schema 应带 i 意图字段")
			}
		}
	}
	if !found {
		t.Fatal("Specs 应包含 grep_ex")
	}
}

func TestGrepExToolSchemaAndIdentity(t *testing.T) {
	tool := grepExTool{}
	if tool.Name() != "grep_ex" {
		t.Fatalf("Name = %q", tool.Name())
	}
	if tool.Mutating() {
		t.Fatal("grep_ex 应为只读")
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Fatal("Description 不应为空")
	}

	schema := tool.Schema()
	if !json.Valid([]byte(schema)) {
		t.Fatalf("Schema 不是合法 JSON：%s", schema)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		t.Fatal(err)
	}
	required, ok := doc["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "pattern" {
		t.Fatalf("required 应为 [pattern]，实际 %v", doc["required"])
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties 缺失：%v", doc)
	}
	for _, key := range []string{"pattern", "path", "include", "context", "before", "after", "case_sensitive", "output_mode"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("properties 缺少 %q", key)
		}
	}
}
