package app

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/mcp"
	"example.com/argentgo/internal/tools"
)

// newResourceToolset 构造一个只带 MCP 绑定与空内置注册表的工具集，
// 让资源工具的 Specs/Run 路径可在无网络、无进程的情况下测试。
func newResourceToolset(t *testing.T, clients map[string]*fakeMCPClient) *toolset {
	t.Helper()
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{tools.New(t.TempDir(), false)}}}
	for name, client := range clients {
		set.mcp = append(set.mcp, mcpBinding{client: client, server: name, spec: llm.ToolSpec{}})
	}
	return set
}

func TestMCPResourceSpecsAdvertised(t *testing.T) {
	set := newResourceToolset(t, map[string]*fakeMCPClient{"docs": {}})
	specs := set.Specs()
	want := map[string]bool{mcpListResourcesTool: false, mcpReadResourceTool: false}
	for _, spec := range specs {
		if _, ok := want[spec.Function.Name]; ok {
			want[spec.Function.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("spec %q not advertised", name)
		}
	}

	empty := &toolset{base: &multiRootTools{regs: []*tools.Registry{tools.New(t.TempDir(), false)}}}
	for _, spec := range empty.Specs() {
		if spec.Function.Name == mcpListResourcesTool || spec.Function.Name == mcpReadResourceTool {
			t.Fatalf("resource spec advertised without MCP servers: %q", spec.Function.Name)
		}
	}
}

func TestListMCPResourcesFiltersServer(t *testing.T) {
	docs := &fakeMCPClient{resources: []mcp.Resource{{URI: "file:///readme.md", Name: "readme"}}}
	logs := &fakeMCPClient{resources: []mcp.Resource{{URI: "file:///app.log", Name: "app log"}}}
	set := newResourceToolset(t, map[string]*fakeMCPClient{"docs": docs, "logs": logs})

	got, err := set.Run(context.Background(), mcpListResourcesTool, `{"server":"docs"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(got, "file:///readme.md") || strings.Contains(got, "file:///app.log") {
		t.Fatalf("server filter failed: %s", got)
	}
	if !strings.Contains(got, "untrusted") {
		t.Fatalf("untrusted marker missing: %s", got)
	}
}

func TestReadMCPResourceRoutesByURI(t *testing.T) {
	empty := &fakeMCPClient{}
	docs := &fakeMCPClient{contents: map[string][]mcp.ResourceContent{
		"file:///a.md": {{URI: "file:///a.md", Text: "你好"}},
	}}
	set := newResourceToolset(t, map[string]*fakeMCPClient{"empty": empty, "docs": docs})

	got, err := set.Run(context.Background(), mcpReadResourceTool, `{"uri":"file:///a.md"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(got, `"server":"docs"`) || !strings.Contains(got, "你好") {
		t.Fatalf("route = %s", got)
	}

	if _, err := set.Run(context.Background(), mcpReadResourceTool, `{"uri":""}`); err == nil {
		t.Fatal("empty uri must fail")
	}
	if _, err := set.Run(context.Background(), mcpReadResourceTool, `{"uri":"file:///missing.md"}`); err == nil {
		t.Fatal("missing resource must fail on all servers")
	}
}

func TestCapMCPResourceTextKeepsUTF8Valid(t *testing.T) {
	long := strings.Repeat("中文资源", mcpResourceMaxRunes)
	got := capMCPResourceText(long)
	if !utf8.ValidString(got) {
		t.Fatal("truncated text is not valid UTF-8")
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Fatal("long text must be marked as truncated")
	}
	if capMCPResourceText("你好") != "你好" {
		t.Fatal("short text must be returned verbatim")
	}
}
