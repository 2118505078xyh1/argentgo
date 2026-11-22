// Package mcp 通过 stdio 或 HTTP/SSE 与外部工具服务器通信
// Model Context Protocol，并用一个小巧的 Client 接口暴露其工具。
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

const (
	// defaultTimeout 在 context 不带截止时间时限制一次请求。
	defaultTimeout = 30 * time.Second
	// protocolVersion 是本客户端宣告的 MCP 版本。
	protocolVersion = "2024-11-05"
	// clientName 在 clientInfo 中报告给服务器。
	clientName = "argentgo"
	// clientVersion 在 clientInfo 中报告给服务器。
	clientVersion = "0.8.1"
	// maxListPages 限制游标驱动的列表分页。一个故障服务器不断
	// 生成新游标，不得让发现过程挂起或无界增长内存；
	// 普通服务器只用几页。
	maxListPages = 1000
)

// mcpListChangedRefreshEnv 是工具清单变更跟踪的 opt-in 开关。工具输出与工具目录
// 都来自不可信的外部服务器，默认不跟踪它的 list_changed 通知，行为逐字节不变；
// 只有值为 "1"（允许首尾空白）时才在 Connect 时把通知回调接到传输的读取循环上。
const mcpListChangedRefreshEnv = "ARGENT_MCP_LIST_CHANGED_REFRESH"

// MCPHealthEnv 是 MCP 运行时健康接线的 opt-in 环境变量名。装配层（internal/app）
// 据此决定是否给 Spec.HealthTracking 赋值并把健康快照持久化，诊断层（doctor）
// 据此决定是否读取这份快照。默认未设置（或非 "1"）时两段接线都是空操作：
// 不挂接 HealthTracker、不写文件、不新增诊断输出，行为逐字节不变。
// 常量放在本包使「开关语义」与 HealthTracking 的提供方同处一处，避免两处分叉。
const MCPHealthEnv = "ARGENT_MCP_HEALTH"

// MCPHealthReportFile 是运行时健康快照在存储目录下的文件名。写入端（internal/app）
// 与读取端（doctor）共用这一常量，避免两处文件名漂移导致诊断读不到快照。
const MCPHealthReportFile = "mcp-health.json"

// ListChangedRefreshEnabled 报告是否开启 notifications/tools/list_changed 跟踪。
// mcp 包与接线层（internal/app）共用这一判断，避免开关语义在两处分叉。
// 每次调用都读取环境，与本包其他 opt-in 能力的读取方式保持一致。
func ListChangedRefreshEnabled() bool {
	return strings.TrimSpace(os.Getenv(mcpListChangedRefreshEnv)) == "1"
}

// ErrUnsupportedTransport 报告本包不支持的传输方式。
var ErrUnsupportedTransport = errors.New(`mcp: transport must be "stdio" or "http"`)

// Spec 描述如何连接一台服务器。
type Spec struct {
	// Name 标识服务器；它为工具提供命名空间。
	Name string
	// Transport 是 "stdio" 或 "http"（空值会被推断）。
	Transport string
	// Command 是要启动的 stdio 程序。
	Command string
	// Args 是 stdio 程序的参数。
	Args []string
	// Dir 是 stdio 子进程的工作目录（仅 stdio 使用，HTTP 忽略）。
	// 空值沿用 agent 进程的当前目录，与历史行为逐字节一致；
	// 非空时插件 bundle 的相对路径脚本按 bundle 目录而非 agent cwd 解析。
	Dir string
	// Env 条目（"KEY=VALUE"）会加到子进程继承的环境中。
	Env []string
	// URL 是 HTTP 端点。旧的 SSE 端点会宣告请求 POST 到哪里；
	// 流式端点则直接接受 JSON-RPC POST。
	URL string
	// Headers 是额外的 HTTP 头部（stdio 忽略）。
	Headers map[string]string
	// ProtocolVersion 可选地覆盖 initialize 请求中宣告的协议版本。
	// 空值或无效值回退到默认值，再回退到 ARGENT_MCP_PROTOCOL_VERSION。
	// 参见 effectiveProtocolVersion。
	ProtocolVersion string
	// Timeout 限制一次请求；零值表示 30 秒。
	Timeout time.Duration
	// DiscoveryTimeout 是可选的每服务器发现超时覆盖。
	// 零值选择分级默认值（stdio 30 秒 / 远程 5 秒）；正值
	// 会被钳制到 [1s, 2m]。参见 EffectiveDiscoveryTimeout。
	DiscoveryTimeout time.Duration
	// Enabled 为 nil 时默认为 true。显式 false 会禁用该服务器：
	// Connect 会拒绝它，且它会被从 Specs/Tools 视图中省略。使用指针是
	// 为了让所有已有的 Spec 字面量保持其零值含义。
	Enabled *bool
	// ReadOnly 在任何请求发出之前，阻止调用看起来会修改状态的工具。
	// 参见 MarkMutating/IsMutating。
	ReadOnly bool
	// AllowedTools 是工具白名单；空值表示允许所有工具。条目可以是
	// 简单名（"search"）或限定名（"server__search"）。
	AllowedTools []string
	// DeniedTools 从允许集合中移除名称；拒绝优先于允许。
	DeniedTools []string
	// CompactSchemaMaxBytes 是可选的 inputSchema 体积上限（字节）。
	// 大于 0 时，ListTools 会对清理后的 Schema 调用 compactSchema 做
	// 分级有损压缩；零值表示关闭，工具定义保持原有行为。
	CompactSchemaMaxBytes int
	// ToolCacheTTL 是可选的工具发现列表缓存有效期。
	// 大于 0 时，ListTools 会在有效期内复用上一次成功的 tools/list
	// 结果，过期后重新发现；零值表示关闭，ListTools 走原路径，
	// 行为逐字节不变。缓存按服务器隔离，每台服务器各持一份。
	ToolCacheTTL time.Duration
	// HealthTracking 开启工具发现的健康观测。默认 false：不挂接任何
	// tracker，discoverTools 不执行 Observe，行为与历史逐字节一致。
	// 为 true 时每次实际发现（成功或失败）都会向内部 HealthTracker
	// 记录一次快照，调用方可通过 HealthReporter 选择性读取。
	HealthTracking bool
}

// ServerInfo 是 initialize 返回的元数据。
type ServerInfo struct {
	Name            string
	Version         string
	ProtocolVersion string
	Instructions    string
	Capabilities    json.RawMessage
}

// Tool 是服务器宣告的一个工具。
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Schema 是规范化的 inputSchema：它总有 "type"，且不含
	// 格式错误的 "properties"/"required"。可以直接放入
	// 提供方工具规格（参见 internal/app 的 mcpBinding 接线）。
	Schema json.RawMessage `json:"schema,omitempty"`
	Server string          `json:"server,omitempty"`
	// ReadOnly 对应 MCP 的 annotations.readOnlyHint 位。
	ReadOnly bool `json:"read_only,omitempty"`
	// Annotations 是该工具完整的 MCP annotations 解析结果（加法字段）。
	// 它与 ReadOnly 并存：ReadOnly 保留历史 readOnlyHint 行为，本字段额外提供
	// destructiveHint/idempotentHint/openWorldHint/title 等信息。
	Annotations ToolAnnotations `json:"annotations,omitempty"`

	// RawInputSchema 是服务器宣告的未经修改的 inputSchema。
	// 它是加法字段，为空时省略；构造提供方规格的调用方
	// 应继续使用已经清理过的 Schema。
	RawInputSchema json.RawMessage `json:"raw_input_schema,omitempty"`
	// OutputSchema 是服务器的 outputSchema 成员，原样透传。
	// 它是加法字段，为空时省略。
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`

	// RawName 是服务器广播的原始工具名，供协议调用（tools/call）使用。
	// 面向模型的名字经过归一化（转义、截断、重名消解）后可能与原名不同，
	// 调用服务器时必须使用原名，否则服务器无法识别。加法字段，为空时等同于 Name。
	RawName string `json:"raw_name,omitempty"`
}

// Resource 是服务器宣告的一个资源。
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// Client 是一台已连接的服务器。必须先成功调用 Initialize，其他
// 调用才有意义；Close 可以安全地多次调用。
type Client interface {
	Initialize(ctx context.Context) (ServerInfo, error)
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (string, error)
	ListResources(ctx context.Context) ([]Resource, error)
	Close() error
}

// transport 是 Client 背后的线协议实现。
type transport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	Close() error
}

// Namespaced 把服务器名和工具名拼接为展示给模型的名字。
func Namespaced(server, tool string) string {
	return server + "__" + tool
}

// Connect 打开 spec 指定的传输。MCP 握手不属于
// Connect：请在 ListTools 或 CallTool 之前调用 Initialize。
func Connect(ctx context.Context, spec Spec) (Client, error) {
	if !spec.IsEnabled() {
		return nil, fmt.Errorf("%w: %q", ErrDisabled, spec.Name)
	}
	kind := strings.ToLower(strings.TrimSpace(spec.Transport))
	if kind == "" {
		if spec.Command != "" {
			kind = "stdio"
		} else {
			kind = "http"
		}
	}
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = "mcp"
	}

	var tr transport
	switch kind {
	case "stdio":
		if strings.TrimSpace(spec.Command) == "" {
			return nil, fmt.Errorf("mcp %q: stdio transport needs a command", spec.Name)
		}
		stdio, err := newStdioTransport(spec)
		if err != nil {
			return nil, err
		}
		tr = stdio
	case "http", "sse":
		if strings.TrimSpace(spec.URL) == "" {
			return nil, fmt.Errorf("mcp %q: http transport needs a URL", spec.Name)
		}
		httpTransport, err := newHTTPTransport(ctx, spec)
		if err != nil {
			return nil, err
		}
		tr = httpTransport
	default:
		return nil, fmt.Errorf("%w (got %q)", ErrUnsupportedTransport, spec.Transport)
	}

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	c := &client{spec: spec, tr: tr, timeout: timeout}
	// 按 Spec 字段决定是否挂接健康追踪器；默认关闭时 health 保持 nil，
	// 后续发现路径不会执行任何观测，也没有额外 I/O。
	if spec.HealthTracking {
		c.health = &HealthTracker{}
	}
	// opt-in 默认关闭：未设置环境变量时不安装任何回调，读取循环里
	// onNotification 保持 nil，handleOne 的分发路径逐字节不变。
	if ListChangedRefreshEnabled() {
		c.trackListChanges = true
		attachNotificationHandler(tr, c.onNotification)
	}
	return c, nil
}

// attachNotificationHandler 把回调安装到传输背后的 waiters 上。两种传输都在
// 各自的构造函数里启动了读取循环，因此这里通过带锁的 setOnNotification 安装，
// 与已经在读取 method 成员的 goroutine 之间不会发生数据竞争。
func attachNotificationHandler(tr transport, cb func(method string)) {
	switch t := tr.(type) {
	case *stdioTransport:
		t.waiters.setOnNotification(cb)
	case *httpTransport:
		t.waiters.setOnNotification(cb)
	}
}

// onNotification 处理服务器发起的通知。它运行在传输的读取循环里，因此只做
// 一次原子递增：真正的 tools/list 留给下一次 ListTools/RefreshTools 触发，
// 这里绝不执行发现、I/O 或可能阻塞的操作。
func (c *client) onNotification(method string) {
	if isToolListChanged(method) {
		c.revision.Bump()
	}
}

// syncCatalogRevision 比较通知推进的版本号与上次同步的版本号；不同则使
// 工具缓存失效，迫使下一次 ListTools 重新执行 tools/list。用 CAS 更新避免
// 并发调用把 seenRevision 写回旧值而重复失效。仅在跟踪开启时调用。
func (c *client) syncCatalogRevision() {
	for {
		seen := c.seenRevision.Load()
		rev := c.revision.Revision()
		if rev == seen {
			return
		}
		if c.seenRevision.CompareAndSwap(seen, rev) {
			c.toolCache.invalidate()
			return
		}
	}
}

// client 在传输之上实现 Client。
type client struct {
	spec    Spec
	tr      transport
	timeout time.Duration

	// toolCache 在 Spec.ToolCacheTTL > 0 时缓存工具发现结果；
	// 关闭时 ListTools 完全不触碰它。
	toolCache toolCache

	// health 仅在 Spec.HealthTracking 为 true 时挂接；为 nil 时
	// discoverTools 不执行任何观测，零 I/O、行为逐字节不变。
	health *HealthTracker

	// revision 是工具目录的逻辑版本号：仅当开关开启时接到通知回调上的
	// onNotification 会在收到 list_changed 时调用 Bump。seenRevision 是
	// ListTools 上一次同步过的版本号，两者不等就说明目录可能已变化。
	// 两者都是纯计数器；client 一旦投入使用就不可复制。
	revision     ToolCatalogRevision
	seenRevision atomic.Uint64
	// trackListChanges 决定 Connect 是否安装通知回调、ListTools 是否参与
	// 版本号比较。默认 false 时下面两条路径都完全不触碰，行为逐字节不变。
	trackListChanges bool

	// mu 保护 Tools 暴露的最后一次成功列出的工具视图，以及
	// 从 initialize 响应解析出的能力。
	mu    sync.RWMutex
	tools []Tool
	caps  ServerCapabilities
}

func (c *client) Initialize(ctx context.Context) (ServerInfo, error) {
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	params := map[string]any{
		"protocolVersion": effectiveProtocolVersion(c.spec),
		// 只宣告真正实现的能力：handleOne 会丢弃所有服务器发起的请求（没有
		// roots/list、sampling/createMessage 或 elicitation/create 处理器），
		// 此时若声称支持这些能力，合规服务器会发来我们无法回复的请求。
		// 目前传入零值选项，因此声明空对象，与历史行为逐字节一致。
		"capabilities": clientCapabilities(clientCapabilityOptions{}),
		"clientInfo":   map[string]any{"name": clientName, "version": clientVersion},
	}
	// 初始化是幂等的发现调用：选择加入后可对短暂故障重试；
	// 默认 maxAttempts=1，请求序列与错误文本均不变。
	raw, err := c.callWithRetry(ctx, "initialize", params)
	if err != nil {
		return ServerInfo{}, c.wrap(err, "initialize")
	}
	var result struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Instructions    string          `json:"instructions"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ServerInfo{}, fmt.Errorf("mcp %q: decode initialize result: %w", c.spec.Name, err)
	}
	info := ServerInfo{
		Name:            result.ServerInfo.Name,
		Version:         result.ServerInfo.Version,
		ProtocolVersion: result.ProtocolVersion,
		Instructions:    result.Instructions,
		Capabilities:    result.Capabilities,
	}
	// 在边界处解析一次并记住结果：ListTools 和
	// ListResources 用它跳过服务器无法处理的请求。保留 ServerInfo 中的
	// 原始载荷不变，供需要它的调用方使用。
	c.mu.Lock()
	c.caps = ParseCapabilities(result.Capabilities)
	c.mu.Unlock()
	if err := c.tr.notify(ctx, "notifications/initialized", nil); err != nil {
		return ServerInfo{}, c.wrap(err, "notifications/initialized")
	}
	return info, nil
}

// capabilitySkipped 报告是否应抑制某个列表请求，因为服务器
// 宣告了一些能力但没有宣告这一项。没有发送能力、只发送空 "{}"
// 或只有未知键的服务器被视为未知，仍会收到请求，因此旧服务器
// 和宣告不足的服务器
// 仍能工作。
func (c *client) capabilitySkipped(cap string) bool {
	c.mu.RLock()
	caps := c.caps
	c.mu.RUnlock()
	return caps.Enumerated() && !caps.Supports(cap)
}

// ListTools 返回服务器宣告的工具。
//
// 当 Spec.ToolCacheTTL > 0 时启用带 TTL 的发现缓存：有效期内直接复用
// 上一次成功的结果，过期后重新发现；并发调用由 toolCache 收敛为单次
// 发现（single-flight）。TTL 为 0（默认）时不做任何缓存，逐字节走
// discoverTools 的原路径。
//
// 当 opt-in 的 list_changed 跟踪开启时，读缓存前先比较目录版本号：通知推进过
// 版本号就先使缓存失效，保证新宣告的工具能在本次重新发现中返回。开关关闭时
// 不触碰 revision 与 toolCache，行为逐字节不变。
func (c *client) ListTools(ctx context.Context) ([]Tool, error) {
	if c.trackListChanges {
		c.syncCatalogRevision()
	}
	ttl := c.spec.ToolCacheTTL
	if ttl <= 0 {
		return c.discoverTools(ctx)
	}
	if tools, ok := c.toolCache.get(); ok {
		return tools, nil
	}
	// get 返回 false 表示本调用者已成为刷新者：必须实际发现一次，
	// 并在结束时发布结果或唤醒等待者。
	tools, err := c.discoverTools(ctx)
	if err != nil {
		c.toolCache.fail()
		return nil, err
	}
	c.toolCache.set(tools, ttl)
	return tools, nil
}

// discoverTools 执行一次真正的工具发现（原 ListTools 的完整实现）。
// 保持独立是为了让默认路径与缓存开启时共用同一段代码，避免行为漂移。
func (c *client) discoverTools(ctx context.Context) ([]Tool, error) {
	if c.capabilitySkipped("tools") {
		return nil, nil
	}
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	tools := make([]Tool, 0)
	// 服务器可能用不透明的 nextCursor 对 tools/list 分页。跟随每一
	// 页，使第一页之后的工具不会悄悄丢失。逐项解码：
	// 单个格式错误的工具不应拖垮整张表。
	err := c.forEachListPage(ctx, "tools/list", func(raw json.RawMessage) (json.RawMessage, error) {
		var result struct {
			Tools []json.RawMessage `json:"tools"`
			Next  json.RawMessage   `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("mcp %q: decode tools/list result: %w", c.spec.Name, err)
		}
		for _, itemRaw := range result.Tools {
			var item struct {
				Name         string          `json:"name"`
				Description  string          `json:"description"`
				InputSchema  json.RawMessage `json:"inputSchema"`
				OutputSchema json.RawMessage `json:"outputSchema"`
				Annotations  json.RawMessage `json:"annotations"`
			}
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				continue // 只跳过坏条目，保留表的其余部分
			}
			// 保持历史容错语义：annotations 若存在且不能解码成带布尔
			// readOnlyHint 的对象（或 null），视为坏工具跳过。
			if !annotationsLegacyDecodable(item.Annotations) {
				continue
			}
			annotations := parseToolAnnotations(item.Annotations)
			if strings.TrimSpace(item.Name) == "" {
				continue
			}
			schema := normalizeInputSchema(item.InputSchema)
			if c.spec.CompactSchemaMaxBytes > 0 {
				schema = compactSchema(schema, c.spec.CompactSchemaMaxBytes)
			}
			tools = append(tools, Tool{
				Name:           item.Name,
				RawName:        item.Name,
				Description:    item.Description,
				Schema:         schema,
				Server:         c.spec.Name,
				ReadOnly:       annotations.ReadOnly,
				RawInputSchema: append(json.RawMessage(nil), item.InputSchema...),
				OutputSchema:   append(json.RawMessage(nil), item.OutputSchema...),
				Annotations:    annotations,
			})
		}
		return nextCursor(result.Next), nil
	})
	if err != nil {
		if c.health != nil {
			c.health.Observe(false, 0, err)
		}
		return nil, err
	}
	// 在这里应用每服务器权限，使调用方永远看不到服务器配置
	// 不允许的工具。保持宣告顺序以获得稳定输出。
	tools = FilterTools(c.spec, tools)
	if c.health != nil {
		// 记录过滤后的工具数：调用方看到的工具数与快照一致。
		c.health.Observe(true, len(tools), nil)
	}
	c.mu.Lock()
	c.tools = append([]Tool(nil), tools...)
	c.mu.Unlock()
	return tools, nil
}

// mcpInjectionScanEnv 是 MCP 工具输出注入扫描的开关。工具输出是不可信
// 的外部文本，进入模型上下文前有注入风险；默认开启（对用户是纯增益），
// 只有显式设为 "0"（允许首尾空白）时才关闭。关闭时输出与历史逐字节一致。
const mcpInjectionScanEnv = "ARGENT_MCP_INJECTION_SCAN"

// mcpInjectionWarning 是命中高风险注入时加在原文前的可读警示。只警示、不改写，
// 也不过滤原文中的任何字节——是否遵从注入内容由模型自行判断。
const mcpInjectionWarning = "[注意：工具输出含疑似指令注入]"

// mcpInjectionMediumWarning 是中风险注入的可读警示，仅在 opt-in
// ARGENT_MCP_INJECTION_MEDIUM=1 时加在原文前；默认不发，保持字节不变。
const mcpInjectionMediumWarning = "[注意：工具输出含疑似角色扮演类指令注入]"

// mcpInjectionMediumEnabled 报告是否对中风险命中也发出可见警示。默认关闭：
// 中风险可能是正常讨论里的软信号，默认只有高风险触发警示。
func mcpInjectionMediumEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_MCP_INJECTION_MEDIUM")) == "1"
}

// mcpInjectionScanEnabled 报告是否开启 MCP 工具输出的注入扫描。每次调用都读取
// 环境，便于精确控制开关；这与本包其他 opt-in 能力的读取方式一致。
func mcpInjectionScanEnabled() bool {
	return strings.TrimSpace(os.Getenv(mcpInjectionScanEnv)) != "0"
}

// hasHighSeverityInjection 报告注入命中中是否包含高风险项。中/低风险是软信号，
// 可能出现在正常讨论里，因此只有高风险才触发警示，避免提示词过度敏感。
func hasHighSeverityInjection(findings []security.InjectionFinding) bool {
	for _, f := range findings {
		if f.Severity == security.InjectionSeverityHigh {
			return true
		}
	}
	return false
}

// warnOnPromptInjection 在 enabled 为真且 text 命中高风险注入时，于原文前加一行
// 可读警示；原文字节原样保留，不改写、不过滤。关闭或未命中高风险时原样返回
// text，保证 opt-in 关闭路径逐字节不变。
func warnOnPromptInjection(text string, enabled bool) string {
	if !enabled {
		return text
	}
	// 先剥 Unicode Tag 再检测：不可见 Tag（如 U+E0000）会打断 "ignore
	// previous instructions" 的字节，直接扫描会漏检。检测用剥后的副本，
	// 返回的仍是原文（只加警示、不改写任何字节）。
	findings := security.ScanPromptInjection(security.StripUnicodeTags(text))
	if !hasHighSeverityInjection(findings) {
		// 中风险 opt-in：仅在显式开启且确实命中中风险时加可见警示。
		if mcpInjectionMediumEnabled() {
			for _, f := range findings {
				if f.Severity == security.InjectionSeverityMedium {
					return mcpInjectionMediumWarning + "\n" + text
				}
			}
		}
		return text
	}
	return mcpInjectionWarning + "\n" + text
}

// CallTool 调用一个工具并返回其文本内容，用换行符连接。
// isError 结果作为文本加错误返回，这样想要展示服务器解释的
// 调用方仍能拿到它。
func (c *client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	// 在触碰传输之前 fail closed：只读服务器绝不能
	// 发送修改类调用，被过滤掉的工具绝不能被调用。
	if c.spec.ReadOnly && IsMutating(name) {
		return "", fmt.Errorf("%w: tool %q on server %q looks mutating; call blocked before contacting the server, no request was sent (set readOnly=false in the server config if writes are intended)", ErrReadOnly, name, c.spec.Name)
	}
	if !toolPermitted(c.spec, Tool{Name: name, Server: c.spec.Name}) {
		return "", fmt.Errorf("%w: tool %q on server %q is excluded by allowedTools/deniedTools; no request was sent", ErrToolNotAllowed, name, c.spec.Name)
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return "", fmt.Errorf("mcp %q: tool %q arguments are not valid JSON", c.spec.Name, name)
	}
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	params := map[string]any{"name": name, "arguments": args}
	raw, err := c.tr.call(ctx, "tools/call", params)
	if err != nil {
		return "", c.wrap(err, "tools/call "+name)
	}
	var result struct {
		// 用 callToolBlock 解码，额外保留 image 内容块的 base64 `data`
		// 字段；嵌入的 ContentBlock 字段解码行为与旧代码完全一致。
		Content []callToolBlock `json:"content"`
		IsError bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("mcp %q: decode tools/call result: %w", c.spec.Name, err)
	}

	// 选择加入：设置 ARGENT_MCP_CONTENT_FILTER_MIN_PRIORITY 后，按 audience
	// 与 priority 过滤内容块。未设置或无效时整段过滤都不执行，
	// 输出与历史逐字节一致。
	blocks := result.Content
	if minPriority := contentFilterMinPriority(); minPriority > 0 {
		blocks = filterCallToolBlocks(blocks, minPriority)
	}

	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "resource":
			if block.Resource.Text != "" {
				parts = append(parts, block.Resource.Text)
			} else if blobTextEnabled() && block.Resource.Blob != "" {
				// 选择加入（ARGENT_MCP_BLOB_TEXT）：文本型 blob 直接当文本用，
				// 二进制 blob 输出安全占位符，非法 base64 降级为占位符而非报错。
				// 开关默认关闭，因此默认输出与旧行为逐字节一致。
				parts = append(parts, blobTextFromResource(block.Resource))
			} else if dir := blobArtifactDir(); dir != "" && block.Resource.Blob != "" {
				// 仅选择加入（ARGENT_MCP_BLOB_ARTIFACT_DIR）。没有配置目录时
				// 会跳过该分支，因此输出与旧行为逐字节
				// 一致。保存失败是静默的，因此坏 blob 绝不会让
				// 工具调用失败。
				if marker := saveBlobAsArtifact(dir, block.Resource); marker != "" {
					parts = append(parts, marker)
				}
			}
		case "image":
			parts = append(parts, imageBlockPlaceholder(block))
		}
	}
	out := strings.Join(parts, "\n")
	if out == "" {
		out = string(raw)
	}
	// 选择加入：限制非常大的 MCP 结果，使一个话痨服务器无法淹没
	// 模型上下文。未设置 ARGENT_MCP_SPILL_DIR 时这是空操作，因此
	// 旧输出（以及所有现有测试）保持逐字节一致。
	out, _, _ = spillMCPOutput(out, mcpSpillThreshold())
	// 输出硬上限（opt-in ARGENT_MCP_OUTPUT_LIMIT_BYTES，默认 0 = 旧行为）：
	// 20KiB 风格的 head/tail 截断 + 恢复标记，无需配置 spill 目录也能防止
	// 话痨服务器淹没上下文。缺省时原样返回，逐字节不变。
	out = limitMCPOutput(out, mcpOutputLimit())
	// 选择加入：工具输出是不可信的外部文本。这里位于 text/resource/blob/raw
	// 各分支与正常/错误两条返回路径的统一出口，扫描最终进入返回值的文本：
	// 命中高风险注入时仅加一行警示，不改写原文，让模型自行判断。关闭时是空操作。
	out = warnOnPromptInjection(out, mcpInjectionScanEnabled())
	if result.IsError {
		return out, fmt.Errorf("mcp %q: tool %q reported an error: %s", c.spec.Name, name, out)
	}
	return out, nil
}

// ListResources 返回服务器的资源。未实现 resources/list 的
// 服务器会返回空列表，而不是错误。
func (c *client) ListResources(ctx context.Context) ([]Resource, error) {
	if c.capabilitySkipped("resources") {
		return nil, nil
	}
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	resources := make([]Resource, 0)
	err := c.forEachListPage(ctx, "resources/list", func(raw json.RawMessage) (json.RawMessage, error) {
		var result struct {
			Resources []Resource      `json:"resources"`
			Next      json.RawMessage `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("mcp %q: decode resources/list result: %w", c.spec.Name, err)
		}
		resources = append(resources, result.Resources...)
		return nextCursor(result.Next), nil
	})
	if err != nil {
		if methodNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return resources, nil
}

// forEachListPage 每页调用一次 method，跟随服务器返回的不透明游标，
// 直到没有 nextCursor。fn 解码一页并返回下一页的
// 游标（nil 表示最后一页）。第一个请求不带 params，
// 使单页服务器看到与历史完全相同的请求形态。重复的游标或过多
// 页数会阻止故障服务器
// 无限循环或无界增长内存。
func (c *client) forEachListPage(ctx context.Context, method string, fn func(json.RawMessage) (json.RawMessage, error)) error {
	var cursor json.RawMessage
	seen := make(map[string]bool)
	for page := 0; page < maxListPages; page++ {
		var params any
		if len(cursor) > 0 {
			// 游标是不透明的：把 token 原样传回，而不是
			// 强制转换为服务器可能无法识别的字符串。
			params = map[string]any{"cursor": cursor}
		}
		// 列表分页按游标重复请求是幂等的；选择加入后短暂故障可退避重试。
		// 默认仍只发一次，逐字节保持历史行为。
		raw, err := c.callWithRetry(ctx, method, params)
		if err != nil {
			return c.wrap(err, method)
		}
		next, err := fn(raw)
		if err != nil {
			return err
		}
		if len(next) == 0 {
			return nil
		}
		key := string(next)
		if seen[key] {
			return fmt.Errorf("mcp %q: %s repeated pagination cursor %s; stopping", c.spec.Name, method, key)
		}
		seen[key] = true
		cursor = next
	}
	return fmt.Errorf("mcp %q: %s exceeded %d pages; stopping pagination", c.spec.Name, method, maxListPages)
}

// nextCursor 从列表结果中提取不透明的分页 token。缺失、
// null 或空字符串 token 表示服务器没有更多页（nil）。该 token
// 逐字节返回，以便在下一次请求中原样重放。
func nextCursor(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || isJSONNull(t) || string(t) == `""` {
		return nil
	}
	return append(json.RawMessage(nil), t...)
}

func (c *client) Close() error {
	return c.tr.Close()
}

// timeoutContext 限制一次请求，但不覆盖调用方的截止时间。
func (c *client) timeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *client) wrap(err error, method string) error {
	// 用服务器捕获的 stderr 丰富不透明的 stdio 连接失败信息。
	// 不重放进程：stderr 环形缓冲区已经保存了子进程退出前
	// 打印的内容，因此应用简单的"打印错误"路径无需任何改动
	// 就获得了审计所缺失的上下文。
	return c.enrichStderr(fmt.Errorf("mcp %q: %s: %w", c.spec.Name, method, err))
}

// Stderr 返回 stdio 服务器 stderr 的尾部，用于诊断。对 HTTP 客户端
// 或由其他包构造的客户端，它为空。
func Stderr(c Client) string {
	concrete, ok := c.(*client)
	if !ok {
		return ""
	}
	stdio, ok := concrete.tr.(*stdioTransport)
	if !ok {
		return ""
	}
	return stdio.diag.String()
}
