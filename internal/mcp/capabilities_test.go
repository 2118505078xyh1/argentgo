package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestParseCapabilities(t *testing.T) {
	truePtr := func() *bool { v := true; return &v }

	tests := []struct {
		name      string
		raw       string
		declared  bool
		tools     *bool
		prompts   *bool
		resources *bool
	}{
		{
			name: "absent",
			raw:  "",
		},
		{
			name: "whitespace",
			raw:  "  \n\t ",
		},
		{
			name: "null",
			raw:  "null",
		},
		{
			name: "malformed",
			raw:  `{`,
		},
		{
			name: "non object array",
			raw:  `[]`,
		},
		{
			name: "non object string",
			raw:  `"tools"`,
		},
		{
			name:     "empty object",
			raw:      `{}`,
			declared: true,
		},
		{
			name:      "boolean flags",
			raw:       `{"tools":true,"prompts":false,"resources":true}`,
			declared:  true,
			tools:     truePtr(),
			prompts:   boolPtr(false),
			resources: truePtr(),
		},
		{
			name:      "mcp objects mean supported",
			raw:       `{"tools":{"listChanged":true},"resources":{"subscribe":true,"listChanged":true}}`,
			declared:  true,
			tools:     truePtr(),
			resources: truePtr(),
		},
		{
			name:     "explicit false",
			raw:      `{"tools":false}`,
			declared: true,
			tools:    boolPtr(false),
		},
		{
			name:     "null flag stays undeclared",
			raw:      `{"tools":null}`,
			declared: true,
		},
		{
			name:      "keys are case insensitive",
			raw:       `{"Tools":true,"PROMPTS":false," Resources ":true}`,
			declared:  true,
			tools:     truePtr(),
			prompts:   boolPtr(false),
			resources: truePtr(),
		},
		{
			name:     "unknown keys ignored",
			raw:      `{"tools":true,"logging":{},"sampling":{}}`,
			declared: true,
			tools:    truePtr(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCapabilities(json.RawMessage(tt.raw))
			if got.Declared() != tt.declared {
				t.Errorf("Declared() = %v, want %v", got.Declared(), tt.declared)
			}
			if !boolPtrEqual(got.Tools, tt.tools) {
				t.Errorf("Tools = %v, want %v", deref(got.Tools), deref(tt.tools))
			}
			if !boolPtrEqual(got.Prompts, tt.prompts) {
				t.Errorf("Prompts = %v, want %v", deref(got.Prompts), deref(tt.prompts))
			}
			if !boolPtrEqual(got.Resources, tt.resources) {
				t.Errorf("Resources = %v, want %v", deref(got.Resources), deref(tt.resources))
			}
		})
	}
}

func TestServerCapabilitiesSupports(t *testing.T) {
	truthy := true
	falsy := false
	caps := ServerCapabilities{Tools: &truthy, Prompts: &falsy, Resources: nil, declared: true}

	tests := []struct {
		cap  string
		want bool
	}{
		{cap: "tools", want: true},
		{cap: "Tools", want: true},
		{cap: "TOOLS", want: true},
		{cap: " tools ", want: true},
		{cap: "prompts", want: false},   // 显式 false
		{cap: "resources", want: false}, // nil 表示未声明 -> 不支持
		{cap: "unknown", want: false},
		{cap: "", want: false},
	}
	for _, tt := range tests {
		if got := caps.Supports(tt.cap); got != tt.want {
			t.Errorf("Supports(%q) = %v, want %v", tt.cap, got, tt.want)
		}
	}

	// 零值（完全没有 capabilities 对象）不声明任何能力。
	var zero ServerCapabilities
	if zero.Supports("tools") {
		t.Error("zero-value capabilities must not support tools")
	}
}

func TestServerCapabilitiesEnumerated(t *testing.T) {
	truthy := true
	falsy := false
	tests := []struct {
		name string
		caps ServerCapabilities
		want bool
	}{
		{name: "zero value", caps: ServerCapabilities{}, want: false},
		{name: "declared but empty object", caps: ServerCapabilities{declared: true}, want: false},
		{name: "only true flag", caps: ServerCapabilities{Tools: &truthy}, want: true},
		{name: "only false flag", caps: ServerCapabilities{Prompts: &falsy}, want: true},
		{name: "object value", caps: ServerCapabilities{Resources: &truthy}, want: true},
	}
	for _, tt := range tests {
		if got := tt.caps.Enumerated(); got != tt.want {
			t.Errorf("%s: Enumerated() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestInitializeParsesCapabilitiesSkipsUndeclaredLists 验证接线：一台宣告了
// 能力但未宣告 tools 的服务器不会收到 tools/list 调用，
// 而其宣告的 resources 仍会被列出。
func TestInitializeParsesCapabilitiesSkipsUndeclaredLists(t *testing.T) {
	toolsCalled, resourcesCalled := false, false
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"initialize": func(json.RawMessage) (any, error) {
			return map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "fake", "version": "1"},
				"capabilities":    map[string]any{"resources": map[string]any{"listChanged": true}},
			}, nil
		},
		"tools/list": func(json.RawMessage) (any, error) {
			toolsCalled = true
			return map[string]any{"tools": []any{}}, nil
		},
		"resources/list": func(json.RawMessage) (any, error) {
			resourcesCalled = true
			return map[string]any{"resources": []any{map[string]any{"uri": "file:///a"}}}, nil
		},
	}}
	c := newFakeClient("fake", ft, time.Second)

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if len(info.Capabilities) == 0 {
		t.Fatal("raw capabilities payload must be preserved in ServerInfo")
	}
	if !c.caps.Declared() || !c.caps.Supports("resources") || c.caps.Supports("tools") {
		t.Fatalf("capabilities not parsed on initialize: %+v", c.caps)
	}

	tools, err := c.ListTools(context.Background())
	if err != nil || len(tools) != 0 {
		t.Fatalf("undeclared tools should be empty: tools=%+v err=%v", tools, err)
	}
	if toolsCalled {
		t.Error("tools/list must not be sent when the server did not declare tools")
	}

	resources, err := c.ListResources(context.Background())
	if err != nil || len(resources) != 1 {
		t.Fatalf("declared resources should be listed: resources=%+v err=%v", resources, err)
	}
	if !resourcesCalled {
		t.Error("resources/list must be sent when the server declared resources")
	}
}

// TestListRequestsProbeServersWithoutCapabilities 守护保守
// 选择：完全省略 capabilities 成员的服务器仍会被
// 探测，使旧服务器/不合规服务器继续工作。
func TestListRequestsProbeServersWithoutCapabilities(t *testing.T) {
	toolsCalled, resourcesCalled := false, false
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"initialize": func(json.RawMessage) (any, error) {
			return map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "legacy", "version": "1"},
			}, nil
		},
		"tools/list": func(json.RawMessage) (any, error) {
			toolsCalled = true
			return map[string]any{"tools": []any{}}, nil
		},
		"resources/list": func(json.RawMessage) (any, error) {
			resourcesCalled = true
			return map[string]any{"resources": []any{}}, nil
		},
	}}
	c := newFakeClient("legacy", ft, time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if c.caps.Declared() {
		t.Fatal("absent capabilities must stay undeclared")
	}
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if _, err := c.ListResources(context.Background()); err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if !toolsCalled || !resourcesCalled {
		t.Fatalf("servers without capabilities must still be probed: tools=%v resources=%v", toolsCalled, resourcesCalled)
	}
}

// TestEmptyCapabilitiesObjectStillProbes 守护对返回 "{}" 的服务器的
// 保守选择：那是空声明，而非对所有能力的拒绝，
// 因此仍必须尝试 tools/list 和 resources/list。
func TestEmptyCapabilitiesObjectStillProbes(t *testing.T) {
	toolsCalled, resourcesCalled := false, false
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"initialize": func(json.RawMessage) (any, error) {
			return map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "empty", "version": "1"},
				"capabilities":    map[string]any{},
			}, nil
		},
		"tools/list": func(json.RawMessage) (any, error) {
			toolsCalled = true
			return map[string]any{"tools": []any{}}, nil
		},
		"resources/list": func(json.RawMessage) (any, error) {
			resourcesCalled = true
			return map[string]any{"resources": []any{}}, nil
		},
	}}
	c := newFakeClient("empty", ft, time.Second)
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if _, err := c.ListResources(context.Background()); err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if !toolsCalled || !resourcesCalled {
		t.Fatalf("empty capabilities must still be probed: tools=%v resources=%v", toolsCalled, resourcesCalled)
	}
}

func boolPtr(v bool) *bool { return &v }

func deref(p *bool) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
