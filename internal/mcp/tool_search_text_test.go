package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolSearchTextIncludesNameAndDescription(t *testing.T) {
	got := toolSearchText("web_fetch", "Fetch a web page", nil)
	want := "web_fetch web fetch Fetch a web page"
	if got != want {
		t.Fatalf("toolSearchText = %q, want %q", got, want)
	}
}

func TestToolSearchTextIncludesUnderscoreVariant(t *testing.T) {
	got := toolSearchText("web_fetch", "", nil)
	want := "web_fetch web fetch"
	if got != want {
		t.Fatalf("toolSearchText = %q, want %q", got, want)
	}
	// 无下划线的名字不会生成重复的变体，去重后只剩原名。
	if got := toolSearchText("read", "", nil); got != "read" {
		t.Fatalf("toolSearchText(read) = %q, want %q", got, "read")
	}
}

func TestToolSearchTextIncludesSchemaPropertyNamesAndDescriptions(t *testing.T) {
	// url 在 JSON 中写在 method 之前，但结果应按键名字典序（method、url）排列。
	schema := json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"target URL"},"method":{"type":"string","description":"HTTP verb"}}}`)
	got := toolSearchText("http_request", "Make a request", schema)
	want := "http_request http request Make a request method HTTP verb url target URL"
	if got != want {
		t.Fatalf("toolSearchText = %q, want %q", got, want)
	}
	// 参数名本身（url / method）可被检索到，这是本次改动的核心能力。
	for _, param := range []string{"method", "url"} {
		if !strings.Contains(got, param) {
			t.Fatalf("toolSearchText 缺少参数名 %q：%q", param, got)
		}
	}
}

func TestToolSearchTextCJK(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"路径":{"type":"string","description":"要读取的文件路径"}}}`)
	got := toolSearchText("文件_读取", "读取文件内容", schema)
	want := "文件_读取 文件 读取 读取文件内容 路径 要读取的文件路径"
	if got != want {
		t.Fatalf("toolSearchText = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("toolSearchText 产生了非法 UTF-8：%q", got)
	}
}

func TestToolSearchTextTruncatesLongSchemaAtRuneBoundary(t *testing.T) {
	// 每个 "路径" 占 6 字节，用多字节字符填充以同时检验长度上界与 rune 边界安全。
	long := strings.Repeat("路径", 2000)
	schemaBytes, err := json.Marshal(map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"description": long}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := toolSearchText("文件读取", "读取", schemaBytes)
	if len(got) > toolSearchTextMaxBytes {
		t.Fatalf("检索文本长度 %d 超过上界 %d", len(got), toolSearchTextMaxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("长文本截断落在了 UTF-8 字符中间")
	}
	// 即使尾部被截断，开头的工具名仍需保留。
	if !strings.HasPrefix(got, "文件读取") {
		t.Fatalf("截断后丢失了工具名前缀：%q", got)
	}
}

func TestToolSearchTextDeduplicates(t *testing.T) {
	// name、下划线变体、描述、属性键与属性描述都重复 "file"，
	// 去重后应只保留首次出现的片段。
	schema := json.RawMessage(`{"properties":{"file":{"description":"FILE"}}}`)
	got := toolSearchText("file", "file path", schema)
	want := "file file path"
	if got != want {
		t.Fatalf("toolSearchText = %q, want %q", got, want)
	}
}

func TestToolSearchTextEmptyAndInvalidInputs(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		description string
		schema      json.RawMessage
		want        string
	}{
		{name: "全部为空", toolName: "", description: "", schema: nil, want: ""},
		{name: "空 schema 对象", toolName: "x", description: "d", schema: json.RawMessage(`{"type":"object"}`), want: "x d"},
		{name: "非法 JSON", toolName: "x", description: "d", schema: json.RawMessage(`not json`), want: "x d"},
		{name: "properties 非对象", toolName: "x", description: "d", schema: json.RawMessage(`{"properties":[]}`), want: "x d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolSearchText(tt.toolName, tt.description, tt.schema); got != tt.want {
				t.Fatalf("toolSearchText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolSearchTextStableAndDeterministic(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"z":{"description":"Z 描述"},"a":{"description":"A 描述"}}}`)
	first := toolSearchText("t", "d", schema)
	for i := 0; i < 20; i++ {
		if got := toolSearchText("t", "d", schema); got != first {
			t.Fatalf("第 %d 次调用结果不一致：%q vs %q", i, got, first)
		}
	}
	// 属性键必须字典序：a 先于 z。
	if !strings.Contains(first, "a A 描述 z Z 描述") {
		t.Fatalf("属性顺序不稳定：%q", first)
	}
}
