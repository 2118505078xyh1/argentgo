package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeToolName_PlainName(t *testing.T) {
	// 正常名应保持 Namespaced 的 server__tool 形式原样不变。
	if got := normalizeToolName("fs", "read_file", nil); got != "fs__read_file" {
		t.Fatalf("unexpected name: %q", got)
	}
	// 导出入口行为与内部函数一致。
	if got := NormalizeToolName("fs", "read_file", nil); got != "fs__read_file" {
		t.Fatalf("exported entry diverged: %q", got)
	}
}

func TestNormalizeToolName_InvalidCharacters(t *testing.T) {
	// 空格与标点等非安全字符替换为下划线，安全字符（含连字符）保留。
	got := normalizeToolName("my server", "read file!", nil)
	if want := "my_server__read_file_"; got != want {
		t.Fatalf("sanitize mismatch: got %q want %q", got, want)
	}

	// 控制字符同样被替换为下划线。
	got = normalizeToolName("srv", "a\x00b\tc", nil)
	if want := "srv__a_b_c"; got != want {
		t.Fatalf("control chars not sanitized: got %q want %q", got, want)
	}
}

func TestNormalizeToolName_LongNameTruncated(t *testing.T) {
	// 纯 ASCII 长名截断后恰好等于长度上限。
	got := normalizeToolName("srv", strings.Repeat("a", 200), nil)
	if len(got) != MaxToolNameBytes {
		t.Fatalf("expected %d bytes, got %d (%q)", MaxToolNameBytes, len(got), got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated name is not valid UTF-8: %q", got)
	}
}

func TestNormalizeToolName_CJKAndRuneSafeTruncation(t *testing.T) {
	// CJK 属于 Unicode 字母，归一化后应完整保留。
	if got := normalizeToolName("服务", "读取文件", nil); got != "服务__读取文件" {
		t.Fatalf("CJK name not preserved: %q", got)
	}

	// 超长 CJK 名按 rune 边界截断：合法 UTF-8，且每个 CJK 字符占 3 字节，
	// "s__" 前缀占 3 字节，所以总字节数必为 3 的倍数，绝无半个汉字。
	got := normalizeToolName("s", strings.Repeat("文", 100), nil)
	if len(got) > MaxToolNameBytes || !utf8.ValidString(got) {
		t.Fatalf("rune-safe truncation failed: len=%d name=%q", len(got), got)
	}
	if len(got)%3 != 0 {
		t.Fatalf("truncation split a CJK rune: len=%d name=%q", len(got), got)
	}
}

func TestNormalizeToolName_DuplicateConflict(t *testing.T) {
	used := map[string]bool{}
	first := normalizeToolName("github", "deploy", used)  // 先到者占用原名
	second := normalizeToolName("github", "deploy", used) // 后到者获得哈希后缀

	if first != "github__deploy" {
		t.Fatalf("first name changed: %q", first)
	}
	if !strings.HasPrefix(second, "github__deploy_") {
		t.Fatalf("second name lacks hash suffix: %q", second)
	}
	suffix := strings.TrimPrefix(second, "github__deploy_")
	if len(suffix) != toolNameHashHexLen {
		t.Fatalf("unexpected hash length: %q", suffix)
	}
	if !used[first] || !used[second] {
		t.Fatalf("used map not updated: %v", used)
	}
}

func TestNormalizeToolName_StableHashAcrossCalls(t *testing.T) {
	// 同一输入在新分配的 used 上重复调用，结果必须一致。
	a := normalizeToolName("github", "deploy", map[string]bool{})
	b := normalizeToolName("github", "deploy", map[string]bool{})
	if a != b {
		t.Fatalf("not deterministic across calls: %q vs %q", a, b)
	}
	if h1, h2 := stableToolNameHash("github", "deploy"), stableToolNameHash("github", "deploy"); h1 != h2 {
		t.Fatalf("hash not stable: %q vs %q", h1, h2)
	}

	// ("ab","c") 与 ("a","bc") 的拼接名相同，必须用 NUL 分隔区分哈希。
	if h1, h2 := stableToolNameHash("ab", "c"), stableToolNameHash("a", "bc"); h1 == h2 {
		t.Fatalf("hash must separate server/tool fields: %q", h1)
	}
}

func TestNormalizeToolName_HashUsesOriginalInput(t *testing.T) {
	// 三个不同原文归一化到同一个基名，后两个的哈希后缀必须彼此不同。
	used := map[string]bool{}
	first := normalizeToolName("a b", "c", used) // 基名 "a_b__c"
	second := normalizeToolName("a!b", "c", used)
	third := normalizeToolName("a?b", "c", used)

	if first != "a_b__c" {
		t.Fatalf("first name changed: %q", first)
	}
	s2 := strings.TrimPrefix(second, "a_b__c_")
	s3 := strings.TrimPrefix(third, "a_b__c_")
	if s2 == s3 || len(s2) != toolNameHashHexLen || len(s3) != toolNameHashHexLen {
		t.Fatalf("hashes must reflect original inputs: %q %q", s2, s3)
	}
}

func TestNormalizeToolName_DeterministicOrdering(t *testing.T) {
	type entry struct{ server, tool string }
	inputs := []entry{
		{"github", "deploy"},
		{"github", "deploy"},
		{"gitlab", "deploy"},
		{"github", "deploy"},
		{"fs", "read file"},
	}

	run := func() []string {
		// 固定排序后依次注册，模拟确定性排序场景。
		sorted := append([]entry(nil), inputs...)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].server != sorted[j].server {
				return sorted[i].server < sorted[j].server
			}
			return sorted[i].tool < sorted[j].tool
		})
		used := map[string]bool{}
		out := make([]string, 0, len(sorted))
		for _, in := range sorted {
			out = append(out, normalizeToolName(in.server, in.tool, used))
		}
		return out
	}

	first, second := run(), run()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ordering not deterministic: %v vs %v", first, second)
	}
	// 重名必须被消解为不同名字，而不是被丢弃。
	seen := map[string]bool{}
	for _, name := range first {
		if seen[name] {
			t.Fatalf("duplicate name not resolved: %q in %v", name, first)
		}
		seen[name] = true
	}
}

func TestNormalizeToolName_NilAndEmptyInputs(t *testing.T) {
	// nil used 不应 panic，空输入也不应返回空名字。
	_ = normalizeToolName("a", "b", nil)
	if got := normalizeToolName("", "", nil); got == "" {
		t.Fatal("empty inputs produced an empty name")
	}
}

func TestListToolsPopulatesRawName(t *testing.T) {
	// RawName 为加法字段：保留服务器原名，不与面向模型的名字混用。
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			return map[string]any{"tools": []any{
				map[string]any{
					"name":        "read file",
					"inputSchema": map[string]any{"type": "object"},
				},
			}}, nil
		},
	}}
	c := newFakeClient("srv", ft, time.Second)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[0].Name != "read file" || tools[0].RawName != "read file" {
		t.Fatalf("raw name not preserved: %+v", tools[0])
	}
}
