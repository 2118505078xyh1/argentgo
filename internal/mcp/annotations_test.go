package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestParseToolAnnotationsAllFields(t *testing.T) {
	raw := json.RawMessage(`{"title":"delete file","readOnlyHint":false,"destructiveHint":true,"idempotentHint":false,"openWorldHint":true}`)
	got := parseToolAnnotations(raw)
	want := ToolAnnotations{
		ReadOnly:    false,
		Destructive: true,
		Idempotent:  false,
		OpenWorld:   true,
		Title:       "delete file",
	}
	if got != want {
		t.Fatalf("parseToolAnnotations() = %+v, want %+v", got, want)
	}
}

func TestParseToolAnnotationsDefaults(t *testing.T) {
	// MCP 规范：destructiveHint 与 openWorldHint 缺省为 true，其余为 false。
	base := ToolAnnotations{Destructive: true, OpenWorld: true}
	withTitle := base
	withTitle.Title = "only title"
	cases := []struct {
		name string
		raw  string
		want ToolAnnotations
	}{
		{"空对象", `{}`, base},
		{"字段缺失", `{"title":"only title"}`, withTitle},
		{"整体缺省", ``, base},
		{"null", `null`, base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseToolAnnotations(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("parseToolAnnotations(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseToolAnnotationsInvalidTypes(t *testing.T) {
	// 整个 annotations 不是对象：按“未声明”返回规范默认值。
	wantDefault := ToolAnnotations{Destructive: true, OpenWorld: true}
	for _, raw := range []string{`7`, `"oops"`, `[1,2]`, `true`, `{broken`} {
		if got := parseToolAnnotations(json.RawMessage(raw)); got != wantDefault {
			t.Errorf("parseToolAnnotations(%s) = %+v, want %+v", raw, got, wantDefault)
		}
	}
	// 单个字段类型错误只影响该字段，其余字段照常解析。
	got := parseToolAnnotations(json.RawMessage(`{"readOnlyHint":"yes","destructiveHint":false,"idempotentHint":1,"openWorldHint":null,"title":42}`))
	want := ToolAnnotations{Destructive: false, OpenWorld: true}
	if got != want {
		t.Fatalf("混合非法字段：got %+v, want %+v", got, want)
	}
}

func TestParseToolAnnotationsTitleCJK(t *testing.T) {
	const title = "读取文件（简体中文）"
	got := parseToolAnnotations(json.RawMessage(`{"title":"读取文件（简体中文）"}`))
	if got.Title != title {
		t.Fatalf("Title = %q, want %q", got.Title, title)
	}
	if !got.Destructive || !got.OpenWorld {
		t.Fatalf("仅含 title 时丢失规范默认值：%+v", got)
	}
}

func TestParseToolAnnotationsReadOnlyCompat(t *testing.T) {
	// 与既有 readOnlyHint 行为保持一致：仅显式 true 才为真。
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"显式真", `{"readOnlyHint":true}`, true},
		{"显式假", `{"readOnlyHint":false}`, false},
		{"缺省", `{}`, false},
		{"null", `null`, false},
		{"非法类型", `{"readOnlyHint":"yes"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseToolAnnotations(json.RawMessage(tc.raw)).ReadOnly; got != tc.want {
				t.Fatalf("ReadOnly = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListToolsCarriesAnnotations(t *testing.T) {
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			return map[string]any{"tools": []any{
				map[string]any{
					"name":        "full",
					"inputSchema": json.RawMessage(`{"type":"object"}`),
					"annotations": map[string]any{
						"title":           "读取文件",
						"readOnlyHint":    true,
						"destructiveHint": false,
						"idempotentHint":  true,
						"openWorldHint":   false,
					},
				},
				map[string]any{
					"name":        "bare",
					"inputSchema": json.RawMessage(`{"type":"object"}`),
				},
			}}, nil
		},
	}}
	c := newFakeClient("fake", ft, time.Second)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("工具数量 = %d, want 2", len(tools))
	}

	full := tools[0]
	if !full.ReadOnly || !full.Annotations.ReadOnly {
		t.Fatalf("readOnlyHint 未保留：%+v", full)
	}
	wantFull := ToolAnnotations{ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false, Title: "读取文件"}
	if full.Annotations != wantFull {
		t.Fatalf("Annotations = %+v, want %+v", full.Annotations, wantFull)
	}

	// 未声明 annotations 的工具：ReadOnly 兼容旧行为（false），新 DTO 给出规范默认值。
	bare := tools[1]
	if bare.ReadOnly || bare.Annotations.Title != "" {
		t.Fatalf("bare 工具 annotations 未正确默认：%+v", bare)
	}
	wantBare := ToolAnnotations{Destructive: true, OpenWorld: true}
	if bare.Annotations != wantBare {
		t.Fatalf("bare Annotations = %+v, want %+v", bare.Annotations, wantBare)
	}
}
