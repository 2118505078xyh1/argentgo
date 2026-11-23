package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/mcp"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

func TestMCPGatewayEnabledFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false}, {"1", true}, {"true", true}, {"YES", true},
		{"  on  ", true}, {"0", false}, {"false", false}, {"maybe", false},
	}
	for _, tc := range cases {
		env := func(string) string { return tc.raw }
		if got := mcpGatewayEnabled(env); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if mcpGatewayEnabled(nil) {
		t.Fatal("nil env must leave gateway off")
	}
}

func TestMCPGatewaySpecFixed(t *testing.T) {
	servers := []mcp.Spec{{Name: "alpha"}, {Name: "beta"}}
	spec1 := mcpGatewaySpec(servers)
	spec2 := mcpGatewaySpec(servers)
	if !reflect.DeepEqual(spec1, spec2) {
		t.Fatal("gateway spec must be byte-stable across calls")
	}
	if spec1.Function.Name != mcpGatewayToolName {
		t.Fatalf("name = %q", spec1.Function.Name)
	}
	if !strings.Contains(spec1.Function.Description, "alpha") || !strings.Contains(spec1.Function.Description, "beta") {
		t.Fatal("gateway description must list servers")
	}
	if !json.Valid(spec1.Function.Parameters) {
		t.Fatal("gateway schema must be valid JSON")
	}
}

// gatewayTestSet 构造一个纯内存的网关 toolset，无需 Resolve/config/网络。
func gatewayTestSet(t *testing.T, client *fakeMCPClient, sandbox security.SandboxMode) *toolset {
	t.Helper()
	srv := &gatewayServer{client: client, tools: client.tools}
	return &toolset{
		base:           &multiRootTools{},
		gateway:        true,
		gatewayServers: []mcp.Spec{{Name: "svc"}},
		gatewayCatalog: map[string]*gatewayServer{"svc": srv},
		sandbox:        sandbox,
	}
}

func TestMCPGatewayDiscoveryText(t *testing.T) {
	client := &fakeMCPClient{tools: []mcp.Tool{{
		Name: "read_file", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`),
		RawName: "read_file",
	}}}
	set := gatewayTestSet(t, client, security.SandboxWorkspaceWrite)

	out, err := set.runMCPGateway(context.Background(), `{"server":"svc"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read_file", "read a file", "Arguments:"} {
		if !strings.Contains(out, want) {
			t.Errorf("discovery text missing %q:\n%s", want, out)
		}
	}
	if len(client.calls) != 0 {
		t.Fatal("discovery must not call any tool")
	}
}

func TestMCPGatewayExecuteUsesRawName(t *testing.T) {
	client := &fakeMCPClient{tools: []mcp.Tool{{
		Name: "read_file", RawName: "raw_read", ReadOnly: true,
		Annotations: mcp.ToolAnnotations{ReadOnly: true},
	}}}
	set := gatewayTestSet(t, client, security.SandboxWorkspaceWrite)

	_, err := set.runMCPGateway(context.Background(), `{"server":"svc","tool":"read_file","arguments":{"path":"x"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || client.calls[0] != "raw_read" {
		t.Fatalf("CallTool got %v, want [raw_read]", client.calls)
	}
}

func TestMCPGatewayReadOnlySandboxBlocksMutating(t *testing.T) {
	client := &fakeMCPClient{tools: []mcp.Tool{{
		Name: "write_file", RawName: "write_file", // 非只读 → 视为修改类
	}}}
	set := gatewayTestSet(t, client, security.SandboxReadOnly)

	_, err := set.runMCPGateway(context.Background(), `{"server":"svc","tool":"write_file","arguments":{}}`)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only block, got %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatal("blocked call must not reach transport")
	}
}

func TestMCPGatewayUnknownServerOrTool(t *testing.T) {
	set := gatewayTestSet(t, &fakeMCPClient{}, security.SandboxWorkspaceWrite)
	if _, err := set.runMCPGateway(context.Background(), `{"server":"missing"}`); err == nil || !strings.Contains(err.Error(), "unknown or unreachable") {
		t.Fatalf("unknown server: %v", err)
	}
	if _, err := set.runMCPGateway(context.Background(), `{}`); err == nil || !strings.Contains(err.Error(), "server is required") {
		t.Fatalf("missing server: %v", err)
	}
}

func TestMCPGatewayRefreshToolsNoop(t *testing.T) {
	client := &fakeMCPClient{tools: []mcp.Tool{{Name: "t", RawName: "t"}}}
	set := gatewayTestSet(t, client, security.SandboxWorkspaceWrite)
	before := set.Specs()
	set.RefreshTools(context.Background()) // 网关模式应为空操作，不 panic、不改表
	after := set.Specs()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("RefreshTools changed specs in gateway mode")
	}
}

// 确认网关 spec 走的是 llm.ToolSpec（编译期断言）。
var _ llm.ToolSpec = mcpGatewaySpec(nil)
