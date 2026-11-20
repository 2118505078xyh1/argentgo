package mcp

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAssignUniqueToolNames_PlainNames(t *testing.T) {
	// 正常名：原始带命名空间名映射到转义后的 server__tool 形式。
	tools := []Tool{
		{Name: "read_file", Server: "fs"},
		{Name: "search", Server: "web"},
	}
	got := assignUniqueToolNames(tools)
	want := map[string]string{
		"fs__read_file": "fs__read_file",
		"web__search":   "web__search",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected names: got %v want %v", got, want)
	}
}

func TestAssignUniqueToolNames_RawNameFallback(t *testing.T) {
	// Name 为空时回退到 RawName，保证键与归一化结果都不为空。
	tools := []Tool{{RawName: "read file", Server: "srv"}}
	got := assignUniqueToolNames(tools)
	if got["srv__read file"] != "srv__read_file" {
		t.Fatalf("raw name fallback failed: %v", got)
	}
}

func TestAssignUniqueToolNames_LongNameTruncated(t *testing.T) {
	// 长名截断：所有分配结果都不得超过字节上限，且仍是合法 UTF-8。
	tools := []Tool{
		{Name: strings.Repeat("a", 200), Server: "srv"},
		{Name: strings.Repeat("文", 100), Server: "cjk"},
	}
	got := assignUniqueToolNames(tools)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %v", got)
	}
	for original, name := range got {
		if len(name) > MaxToolNameBytes {
			t.Fatalf("%q exceeds %d bytes: %d", original, MaxToolNameBytes, len(name))
		}
		if !utf8.ValidString(name) {
			t.Fatalf("%q is not valid UTF-8: %q", original, name)
		}
	}
}

func TestAssignUniqueToolNames_SanitizationConflict(t *testing.T) {
	// 不同原名转义后撞到同一基名：先到者保留基名，后到者获得稳定哈希后缀。
	tools := []Tool{
		{Name: "read file", Server: "srv"},
		{Name: "read!file", Server: "srv"},
	}
	got := assignUniqueToolNames(tools)
	first := got["srv__read file"]
	second := got["srv__read!file"]
	if first != "srv__read_file" {
		t.Fatalf("first name changed: %q", first)
	}
	if !strings.HasPrefix(second, "srv__read_file_") {
		t.Fatalf("second name lacks hash suffix: %q", second)
	}
	if first == second {
		t.Fatalf("conflict not resolved: %q", first)
	}
}

func TestAssignUniqueToolNames_DifferentServersNoHash(t *testing.T) {
	// 不同服务器上的同名工具本就有不同命名空间，不应被误判为冲突。
	tools := []Tool{
		{Name: "deploy", Server: "github"},
		{Name: "deploy", Server: "gitlab"},
	}
	got := assignUniqueToolNames(tools)
	if got["github__deploy"] != "github__deploy" || got["gitlab__deploy"] != "gitlab__deploy" {
		t.Fatalf("unexpected names: %v", got)
	}
}

func TestAssignUniqueToolNames_ExactDuplicateKeepsFirst(t *testing.T) {
	// 完全重复的 (server, name) 只保留一个条目，且不引入哈希后缀。
	tools := []Tool{
		{Name: "deploy", Server: "srv"},
		{Name: "deploy", Server: "srv"},
	}
	got := assignUniqueToolNames(tools)
	if len(got) != 1 || got["srv__deploy"] != "srv__deploy" {
		t.Fatalf("duplicate handling failed: %v", got)
	}
}

func TestAssignUniqueToolNames_CJK(t *testing.T) {
	// CJK 属于 Unicode 字母，应完整保留；超长 CJK 名按 rune 边界截断。
	tools := []Tool{
		{Name: "读取文件", Server: "服务"},
		{Name: strings.Repeat("文", 100), Server: "s"},
	}
	got := assignUniqueToolNames(tools)
	if got["服务__读取文件"] != "服务__读取文件" {
		t.Fatalf("CJK name not preserved: %v", got)
	}
	long := got["s__"+strings.Repeat("文", 100)]
	if len(long) > MaxToolNameBytes || !utf8.ValidString(long) {
		t.Fatalf("rune-safe truncation failed: len=%d name=%q", len(long), long)
	}
	// "s__" 前缀 3 字节，其后每个 CJK 字符 3 字节，总长必为 3 的倍数。
	if len(long)%3 != 0 {
		t.Fatalf("truncation split a CJK rune: len=%d name=%q", len(long), long)
	}
}

func TestAssignUniqueToolNames_Deterministic(t *testing.T) {
	// 相同输入重复调用必须得到逐字节相同的映射；哈希后缀不依赖 map 遍历顺序。
	tools := []Tool{
		{Name: "deploy", Server: "github"},
		{Name: "deploy", Server: "github"},
		{Name: "read file", Server: "fs"},
		{Name: "read!file", Server: "fs"},
		{Name: strings.Repeat("文", 80), Server: "cjk"},
	}
	first := assignUniqueToolNames(tools)
	for i := 0; i < 10; i++ {
		if got := assignUniqueToolNames(tools); !reflect.DeepEqual(got, first) {
			t.Fatalf("not deterministic: run %d = %v, want %v", i, got, first)
		}
	}
	// 映射中的名字两两不同，确保没有遗漏冲突消解。
	seen := map[string]bool{}
	for _, name := range first {
		if seen[name] {
			t.Fatalf("duplicate visible name %q in %v", name, first)
		}
		seen[name] = true
	}
}

func TestAssignUniqueToolNames_Empty(t *testing.T) {
	// 空输入返回空的非 nil 映射，不 panic。
	got := assignUniqueToolNames(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("empty input should yield empty map, got %v", got)
	}
	if got := assignUniqueToolNames([]Tool{}); got == nil || len(got) != 0 {
		t.Fatalf("empty slice should yield empty map, got %v", got)
	}
}
