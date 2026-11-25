package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/mcp"
	"example.com/argentgo/internal/security"
)

// 本文件实现 opt-in 的 MCP 单网关模式（killi 决策 11）：
// schema 里只公布一个固定的 mcp 工具，工具发现结果作为文本返回，服务首次使用时才连接。
// 这样工具列表在整会话中恒定，缓存前缀不会被 list_changed 或服务上线打断。
// 所有逻辑仅在 toolset.gateway 为真时执行；默认关闭时 mcp.go 的既有路径逐字节不变。

// mcpGatewayToolName 是网关模式的固定工具名。
const mcpGatewayToolName = "mcp"

// mcpGatewaySpec 返回一个固定 schema 的网关工具 spec。
// description 里的服务名列表来自会话开始时登记的 spec，因此会话内恒定。
func mcpGatewaySpec(servers []mcp.Spec) llm.ToolSpec {
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name)
	}
	desc := "Discover and call MCP server tools. Pass server (and optionally tool + arguments). " +
		"Call with server only to list that server's tools."
	if len(names) > 0 {
		desc += " Available servers: " + strings.Join(names, ", ") + "."
	}
	spec := llm.ToolSpec{Type: "function"}
	spec.Function.Name = mcpGatewayToolName
	spec.Function.Description = desc
	spec.Function.Parameters = json.RawMessage(`{"type":"object","properties":{"server":{"type":"string","description":"MCP server name"},"tool":{"type":"string","description":"tool name; omit to list the server's tools"},"arguments":{"type":"object","description":"tool arguments object"}},"required":["server"]}`)
	return spec
}

// gatewayCall 是网关工具的参数信封。
type gatewayCall struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// runMCPGateway 处理一次 mcp 网关调用：发现（无 tool）或执行（有 tool）。
func (t *toolset) runMCPGateway(ctx context.Context, args string) (string, error) {
	var call gatewayCall
	if err := json.Unmarshal([]byte(args), &call); err != nil {
		return "", fmt.Errorf("mcp: bad arguments: %w", err)
	}
	call.Server = strings.TrimSpace(call.Server)
	if call.Server == "" {
		return "", fmt.Errorf("mcp: server is required; available servers: %s", t.gatewayServerNames())
	}

	server := t.ensureGatewayServer(ctx, call.Server)
	if server == nil {
		return "", fmt.Errorf("mcp: unknown or unreachable server %q; available servers: %s",
			call.Server, t.gatewayServerNames())
	}

	call.Tool = strings.TrimSpace(call.Tool)
	if call.Tool == "" {
		// 发现：工具清单作为文本返回，不进入 schema。
		return renderGatewayTools(call.Server, server.tools), nil
	}

	tool := findGatewayTool(server.tools, call.Tool)
	if tool == nil {
		return "", fmt.Errorf("mcp: server %q has no tool %q; call with server only to list tools",
			call.Server, call.Tool)
	}

	// 派生带命名空间名，用于只读沙箱判断与审批门。
	effective := mcp.Namespaced(call.Server, tool.Name)
	if len(call.Arguments) == 0 || strings.TrimSpace(string(call.Arguments)) == "" {
		call.Arguments = json.RawMessage(`{}`)
	}

	// 只读沙箱 fail-closed：修改类工具绝不触达传输层。
	if t.sandbox == security.SandboxReadOnly &&
		(security.MutatingTool(effective) || (!tool.ReadOnly && !tool.Annotations.ReadOnly)) {
		return "", fmt.Errorf("%w: tool %q looks mutating and sandbox is read-only; call blocked before contacting the MCP server, no request was sent",
			mcp.ErrReadOnly, effective)
	}

	// 按派生名做审批（readOnly 视为安全、destructive 可强制审批）。
	if err := t.gatewayApproval(ctx, effective, tool, call.Arguments); err != nil {
		return "", err
	}

	// 必须用 RawName 调用：客户端的权限门按原始工具名判定。
	name := tool.RawName
	if name == "" {
		name = tool.Name
	}
	return server.client.CallTool(ctx, name, call.Arguments)
}

// gatewayApproval 用 per-call 链副本按派生工具名做策略/审批判定，避免并发修改共享链。
func (t *toolset) gatewayApproval(ctx context.Context, effective string, tool *mcp.Tool, args json.RawMessage) error {
	if t.guard == nil {
		return nil
	}
	chain := chainFromGuard(t.guard)
	if chain == nil {
		return t.guard.Check(ctx, effective, args)
	}
	callChain := *chain
	// readOnlyHint 的工具视为只读，跳过操作人员提示（拒绝规则仍优先）。
	if tool.ReadOnly || tool.Annotations.ReadOnly {
		callChain.Policy = policyFor(t.gatewayRes, effective)
	}
	// destructiveHint 且 opt-in 开启时强制审批。
	if t.gatewayAnnotationApproval &&
		tool.Annotations.Destructive && !tool.Annotations.ReadOnly {
		callChain.ForceAsk = func(name string) bool { return name == effective }
	}
	return callChain.Check(ctx, effective, args)
}

// ensureGatewayServer 惰性连接一个服务并缓存其工具目录。重复调用不重新发现。
func (t *toolset) ensureGatewayServer(ctx context.Context, name string) *gatewayServer {
	t.mcpMu.RLock()
	if srv, ok := t.gatewayCatalog[name]; ok {
		t.mcpMu.RUnlock()
		return srv
	}
	t.mcpMu.RUnlock()

	var spec mcp.Spec
	found := false
	for _, s := range t.gatewayServers {
		if s.Name == name {
			spec = s
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	attemptCtx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	defer cancel()
	if t.health != nil {
		spec.HealthTracking = true
	}
	client, err := connectMCP(attemptCtx, spec)
	if err == nil {
		_, err = client.Initialize(attemptCtx)
	}
	var tools []mcp.Tool
	if err == nil {
		tools, err = client.ListTools(attemptCtx)
	}
	if err != nil {
		if client != nil {
			_ = client.Close()
		}
		return nil
	}

	srv := &gatewayServer{client: client, tools: tools}
	t.mcpMu.Lock()
	t.gatewayCatalog[name] = srv
	t.clients = append(t.clients, client)
	t.mcpMu.Unlock()
	return srv
}

// gatewayServerNames 返回已登记服务名（稳定顺序，来自会话开始时的登记列表）。
func (t *toolset) gatewayServerNames() string {
	names := make([]string, 0, len(t.gatewayServers))
	for _, s := range t.gatewayServers {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

// findGatewayTool 按面向模型的名字或原始名查找工具。
func findGatewayTool(tools []mcp.Tool, name string) *mcp.Tool {
	for i := range tools {
		if tools[i].Name == name || tools[i].RawName == name {
			return &tools[i]
		}
	}
	return nil
}

// renderGatewayTools 把工具清单渲染为模型可读的文本（不进入 schema）。
func renderGatewayTools(server string, tools []mcp.Tool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tools on MCP server %q:\n", server)
	if len(tools) == 0 {
		b.WriteString("  (no tools)\n")
		return b.String()
	}
	for _, tool := range tools {
		fmt.Fprintf(&b, "## %s\n%s\n", tool.Name, strings.TrimSpace(tool.Description))
		if len(tool.Schema) > 0 {
			fmt.Fprintf(&b, "Arguments: %s\n", string(tool.Schema))
		}
		b.WriteString("\n")
	}
	return b.String()
}
