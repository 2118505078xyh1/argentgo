package tools

// DAP 调试工具集。它们把 Debug Adapter Protocol 的会话控制能力暴露给模型：
// 启动适配器、设置断点、继续/单步、查询线程/栈/变量以及停止会话。
//
// 依赖契约
// -------------------
// internal/tools 不 import internal/app（分层规则禁止），也不 import
// internal/dap 里尚未定稿的高层 Session API；只有低层的 dap.Start / *dap.Client
// 被用于本文件的具体管理器实现。工具本身只依赖本文件定义的窄接口 DapSessionManager：
// 会话生命周期由 app 注入的管理器负责，DAP 命令语义（initialize/launch/
// setBreakpoints/...）由工具层用 map[string]any 自行构造，不形成任何 import 环。
//
// 与 codegraph_query 一样，八个工具走显式注册范式：RegisterDebug 幂等追加到
// 注册表末尾，绝不被 New/NewWithLimit 调用，默认 Specs()/Names() 与 golden
// 逐字节不变。注入管理器由 SetDap 完成，并遵循 SetLspIde 的原位置重建模式。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/dap"
)

// 工具名集中定义，注册与测试共用，避免字面量漂移。
const (
	debugStartToolName      = "debug_start"
	debugBreakpointToolName = "debug_breakpoint"
	debugContinueToolName   = "debug_continue"
	debugStepToolName       = "debug_step"
	debugThreadsToolName    = "debug_threads"
	debugStackToolName      = "debug_stack"
	debugVariablesToolName  = "debug_variables"
	debugStopToolName       = "debug_stop"

	// dapUnavailable 在未注入管理器时返回，而不是硬错误：让模型看到能力存在，
	// 同时在没有 debug adapter 的环境里不使回合并失败。
	dapUnavailable = "DAP debug integration is not available"

	// defaultDapAdapter 是 ARGENT_DAP_ADAPTER 未设置时的默认适配器命令。
	defaultDapAdapter = "dlv dap"

	// dapCallTimeout 为单条 DAP 请求设置上界，防止失控 adapter 永久挂住工具调用。
	dapCallTimeout = 30 * time.Second
)

// DapSessionManager 是工具与 DAP 会话之间的窄接口，由 app 注入实现。
// 它只负责会话生命周期与原始请求转发；DAP 命令名与参数由工具层构造。
// 小接口使工具测试可以用 fake 实现替代真实进程，无需启动 dlv。
type DapSessionManager interface {
	// Start 启动一个 adapter 会话并返回会话 id。command 为空时由实现按
	// ARGENT_DAP_ADAPTER（缺省 dlv dap）解析；program 为待调试程序。
	Start(ctx context.Context, root, command string, args []string, program string) (string, error)
	// Call 在指定会话上发送一条 DAP 请求并返回原始 body。
	Call(ctx context.Context, sessionID, command string, arguments map[string]any) (json.RawMessage, error)
	// Close 断开并回收指定会话；对未知会话是空操作。
	Close(ctx context.Context, sessionID string) error
}

// dapClientManager 是 DapSessionManager 的生产实现：进程内会话注册表，
// 每个会话持有一个 *dap.Client。管理器由 wireDap 构造一次并注入所有注册表，
// 因此不同注册表工具调用共享同一组会话。
type dapClientManager struct {
	mu       sync.Mutex
	env      func(string) string
	sessions map[string]*dap.Client
	next     int
}

var _ DapSessionManager = (*dapClientManager)(nil)

// NewDapSessionManager 返回一个由 env 提供环境变量的会话管理器。
// env 为 nil 时按未设置处理（使用默认适配器命令）。
func NewDapSessionManager(env func(string) string) DapSessionManager {
	return &dapClientManager{env: env, sessions: make(map[string]*dap.Client)}
}

func (m *dapClientManager) Start(ctx context.Context, root, command string, args []string, program string) (string, error) {
	if strings.TrimSpace(program) == "" {
		return "", errors.New("debug_start: program is required")
	}
	cmd, argv := dapAdapterCommand(command, args, m.env)
	client, err := dap.Start(ctx, cmd, argv, root, nil)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	id := fmt.Sprintf("dap-%d", m.next)
	m.sessions[id] = client
	return id, nil
}

func (m *dapClientManager) Call(ctx context.Context, sessionID, command string, arguments map[string]any) (json.RawMessage, error) {
	m.mu.Lock()
	client := m.sessions[sessionID]
	m.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("unknown dap session %q", sessionID)
	}
	callCtx, cancel := context.WithTimeout(ctx, dapCallTimeout)
	defer cancel()
	return client.Call(callCtx, command, arguments)
}

func (m *dapClientManager) Close(_ context.Context, sessionID string) error {
	m.mu.Lock()
	client := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Close()
}

// dapAdapterCommand 解析适配器命令：显式 command 优先，否则读 ARGENT_DAP_ADAPTER，
// 再退化到 "dlv dap"。命令串按空白拆成可执行文件与默认参数；显式 args 覆盖拆分出的参数。
func dapAdapterCommand(command string, args []string, env func(string) string) (string, []string) {
	if strings.TrimSpace(command) == "" && env != nil {
		command = env("ARGENT_DAP_ADAPTER")
	}
	if strings.TrimSpace(command) == "" {
		command = defaultDapAdapter
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return defaultDapAdapter, nil
	}
	if args != nil {
		return fields[0], append([]string(nil), args...)
	}
	if len(fields) == 1 {
		return fields[0], nil
	}
	return fields[0], append([]string(nil), fields[1:]...)
}

// debugTool 是单个调试工具的实现。每个实例携带自己的管理器与调用闭包，
// 在保持每工具元数据的同时让八个工具 DRY（与 lspIdeTool 同构）。
type debugTool struct {
	name        string
	description string
	schema      string
	mutating    bool
	manager     DapSessionManager
	call        debugCall
}

type debugCall func(ctx context.Context, root string, mgr DapSessionManager, raw json.RawMessage) (string, error)

var _ Tool = (*debugTool)(nil)

func (t *debugTool) Name() string        { return t.name }
func (t *debugTool) Description() string { return t.description }
func (t *debugTool) Schema() string      { return t.schema }
func (t *debugTool) Mutating() bool      { return t.mutating }

func (t *debugTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	if t.manager == nil {
		return dapUnavailable, nil
	}
	out, err := t.call(ctx, root, t.manager, raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", t.name, err)
	}
	return clip(ctx, out), nil
}

// ---- 参数解码 ----

// debugSessionArgs 同时接受 session 与 sessionId 两种键，方便模型沿用习惯写法。
type debugSessionArgs struct {
	Session   string `json:"session"`
	SessionID string `json:"sessionId"`
}

func (a debugSessionArgs) id() string {
	if a.SessionID != "" {
		return a.SessionID
	}
	return a.Session
}

type debugStartArgs struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Program string   `json:"program"`
}

type debugBreakpointArgs struct {
	debugSessionArgs
	Path  string `json:"path"`
	Lines []int  `json:"lines"`
}

type debugStepArgs struct {
	debugSessionArgs
	Kind string `json:"kind"`
}

type debugStackArgs struct {
	debugSessionArgs
	ThreadID int `json:"threadId"`
}

type debugVariablesArgs struct {
	debugSessionArgs
	FrameID            int `json:"frameId"`
	Ref                int `json:"ref"`
	VariablesReference int `json:"variablesReference"`
}

func decodeDebugArgs[T any](raw json.RawMessage) (T, error) {
	var args T
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("bad arguments: %w", err)
	}
	return args, nil
}

// dapBody 把原始响应体转成稳定文本；空 body 用 "{}" 占位。
func dapBody(body json.RawMessage) string {
	if len(body) == 0 {
		return "{}"
	}
	return string(body)
}

// dapCall 在指定会话上发一条请求并返回 body 文本。
func dapCall(ctx context.Context, mgr DapSessionManager, sessionID, command string, arguments map[string]any) (string, error) {
	body, err := mgr.Call(ctx, sessionID, command, arguments)
	if err != nil {
		return "", err
	}
	return dapBody(body), nil
}

// ---- 各工具的调用闭包 ----

func debugStartCall(ctx context.Context, root string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugStartArgs](raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Program) == "" {
		return "", errors.New("program is required")
	}
	id, err := mgr.Start(ctx, root, args.Command, args.Args, args.Program)
	if err != nil {
		return "", err
	}
	// 握手：initialize → launch → configurationDone。任一步失败都回收会话，
	// 避免把半初始化的 adapter 留在注册表里。
	steps := []struct {
		command string
		args    map[string]any
	}{
		{"initialize", map[string]any{
			"clientID": "argentgo", "adapterID": "go", "pathFormat": "path",
			"linesStartAt1": true, "columnsStartAt1": true, "supportsVariableType": true,
		}},
		{"launch", map[string]any{"program": args.Program, "cwd": root, "mode": "debug"}},
		{"configurationDone", map[string]any{}},
	}
	for _, step := range steps {
		if _, err := mgr.Call(ctx, id, step.command, step.args); err != nil {
			_ = mgr.Close(ctx, id)
			return "", fmt.Errorf("handshake %s: %w", step.command, err)
		}
	}
	return fmt.Sprintf("dap session %s started (program %s)", id, args.Program), nil
}

func debugBreakpointCall(ctx context.Context, root string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugBreakpointArgs](raw)
	if err != nil {
		return "", err
	}
	if args.id() == "" {
		return "", errors.New("session is required")
	}
	if len(args.Lines) == 0 {
		return "", errors.New("lines must not be empty")
	}
	// 工作区边界：断点路径必须落在注册根内，拒绝 .. 与绝对路径逃逸。
	abs, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	breakpoints := make([]map[string]any, 0, len(args.Lines))
	for _, line := range args.Lines {
		breakpoints = append(breakpoints, map[string]any{"line": line})
	}
	return dapCall(ctx, mgr, args.id(), "setBreakpoints", map[string]any{
		"source":      map[string]any{"path": abs},
		"breakpoints": breakpoints,
		"lines":       args.Lines,
	})
}

func debugContinueCall(ctx context.Context, _ string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugSessionArgs](raw)
	if err != nil {
		return "", err
	}
	if args.id() == "" {
		return "", errors.New("session is required")
	}
	return dapCall(ctx, mgr, args.id(), "continue", map[string]any{"threadId": 0})
}

func debugStepCall(ctx context.Context, _ string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugStepArgs](raw)
	if err != nil {
		return "", err
	}
	if args.id() == "" {
		return "", errors.New("session is required")
	}
	command := ""
	switch strings.ToLower(strings.TrimSpace(args.Kind)) {
	case "next":
		command = "next"
	case "in", "stepin":
		command = "stepIn"
	case "out", "stepout":
		command = "stepOut"
	default:
		return "", fmt.Errorf("invalid step kind %q (want next|in|out)", args.Kind)
	}
	return dapCall(ctx, mgr, args.id(), command, map[string]any{"threadId": 0})
}

func debugThreadsCall(ctx context.Context, _ string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugSessionArgs](raw)
	if err != nil {
		return "", err
	}
	if args.id() == "" {
		return "", errors.New("session is required")
	}
	return dapCall(ctx, mgr, args.id(), "threads", map[string]any{})
}

func debugStackCall(ctx context.Context, _ string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugStackArgs](raw)
	if err != nil {
		return "", err
	}
	if args.id() == "" {
		return "", errors.New("session is required")
	}
	return dapCall(ctx, mgr, args.id(), "stackTrace", map[string]any{
		"threadId": args.ThreadID, "startFrame": 0, "levels": 100,
	})
}

func debugVariablesCall(ctx context.Context, _ string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugVariablesArgs](raw)
	if err != nil {
		return "", err
	}
	if args.id() == "" {
		return "", errors.New("session is required")
	}
	ref := args.VariablesReference
	if ref == 0 {
		ref = args.Ref
	}
	// 只给 frameId 时先取 scopes，用首个作用域的引用查变量。
	if ref == 0 && args.FrameID != 0 {
		body, err := mgr.Call(ctx, args.id(), "scopes", map[string]any{"frameId": args.FrameID})
		if err != nil {
			return "", err
		}
		var scopes struct {
			Scopes []struct {
				VariablesReference int `json:"variablesReference"`
			} `json:"scopes"`
		}
		if err := json.Unmarshal(body, &scopes); err == nil && len(scopes.Scopes) > 0 {
			ref = scopes.Scopes[0].VariablesReference
		}
	}
	if ref == 0 {
		return "", errors.New("frameId or ref is required")
	}
	return dapCall(ctx, mgr, args.id(), "variables", map[string]any{"variablesReference": ref})
}

func debugStopCall(ctx context.Context, _ string, mgr DapSessionManager, raw json.RawMessage) (string, error) {
	args, err := decodeDebugArgs[debugSessionArgs](raw)
	if err != nil {
		return "", err
	}
	if args.id() == "" {
		return "", errors.New("session is required")
	}
	// disconnect 尽力而为：某些 adapter 不回包；随后总是回收会话。
	_, _ = mgr.Call(ctx, args.id(), "disconnect", map[string]any{"terminateDebuggee": true})
	if err := mgr.Close(ctx, args.id()); err != nil {
		return "", err
	}
	return fmt.Sprintf("dap session %s stopped", args.id()), nil
}

// ---- 构造器 ----

func debugTools(mgr DapSessionManager) []Tool {
	return []Tool{
		&debugTool{
			name: debugStartToolName, mutating: true, manager: mgr,
			description: "启动 debug adapter 会话（initialize → launch → configurationDone），返回会话 id。program 必填；command 默认取 ARGENT_DAP_ADAPTER 或 dlv dap。变更型工具。",
			schema:      `{"type":"object","properties":{"command":{"type":"string","description":"debug adapter 命令，如 dlv dap；省略时取环境变量默认值"},"args":{"type":"array","items":{"type":"string"},"description":"附加的 adapter 命令行参数"},"program":{"type":"string","description":"待调试程序的路径或包（必填）"}},"required":["program"]}`,
			call:        debugStartCall,
		},
		&debugTool{
			name: debugBreakpointToolName, mutating: true, manager: mgr,
			description: "在指定会话的源文件行上设置断点。路径必须落在工作区内，越界会被拒绝。变更型工具。",
			schema:      `{"type":"object","properties":{"session":{"type":"string","description":"debug_start 返回的会话 id"},"path":{"type":"string","description":"工作区内的源文件路径"},"lines":{"type":"array","items":{"type":"integer"},"description":"要设置断点的行号列表"}},"required":["session","path","lines"]}`,
			call:        debugBreakpointCall,
		},
		&debugTool{
			name: debugContinueToolName, mutating: true, manager: mgr,
			description: "让指定会话继续运行到下一个断点。变更型工具。",
			schema:      `{"type":"object","properties":{"session":{"type":"string","description":"会话 id"}},"required":["session"]}`,
			call:        debugContinueCall,
		},
		&debugTool{
			name: debugStepToolName, mutating: true, manager: mgr,
			description: "单步执行：kind=next（下一条）、in（步入）或 out（步出）。变更型工具。",
			schema:      `{"type":"object","properties":{"session":{"type":"string","description":"会话 id"},"kind":{"type":"string","enum":["next","in","out"],"description":"单步种类"}},"required":["session","kind"]}`,
			call:        debugStepCall,
		},
		&debugTool{
			name: debugThreadsToolName, mutating: false, manager: mgr,
			description: "列出指定会话中的线程。只读。",
			schema:      `{"type":"object","properties":{"session":{"type":"string","description":"会话 id"}},"required":["session"]}`,
			call:        debugThreadsCall,
		},
		&debugTool{
			name: debugStackToolName, mutating: false, manager: mgr,
			description: "列出指定线程的调用栈帧。只读。",
			schema:      `{"type":"object","properties":{"session":{"type":"string","description":"会话 id"},"threadId":{"type":"integer","description":"线程 id"}},"required":["session","threadId"]}`,
			call:        debugStackCall,
		},
		&debugTool{
			name: debugVariablesToolName, mutating: false, manager: mgr,
			description: "读取变量：可直接给 variablesReference/ref，或给 frameId 由首个作用域推导。只读。",
			schema:      `{"type":"object","properties":{"session":{"type":"string","description":"会话 id"},"frameId":{"type":"integer","description":"栈帧 id，用于推导变量引用"},"ref":{"type":"integer","description":"variablesReference 简写"},"variablesReference":{"type":"integer","description":"DAP 变量引用"}},"required":["session"]}`,
			call:        debugVariablesCall,
		},
		&debugTool{
			name: debugStopToolName, mutating: true, manager: mgr,
			description: "断开并关闭指定会话（disconnect + close）。变更型工具。",
			schema:      `{"type":"object","properties":{"session":{"type":"string","description":"会话 id"}},"required":["session"]}`,
			call:        debugStopCall,
		},
	}
}

// RegisterDebug 把八个调试工具显式追加到 r 的末尾（幂等）。
// 传入 nil 是空操作。绝不被 New/NewWithLimit 调用，因此默认
// Specs()/Names() 与 golden 逐字节不变；只有显式调用才加入注册表。
func RegisterDebug(r *Registry) {
	if r == nil {
		return
	}
	for _, tool := range debugTools(nil) {
		if _, exists := r.tools[tool.Name()]; exists {
			continue
		}
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
}

// SetDap 安装八个调试工具背后的会话管理器。遵循 SetLspIde 的原位置重建模式：
// 注册顺序与公布的 spec 保持稳定。若尚未调用 RegisterDebug，这里会先完成注册。
// nil 管理器让工具报告不可用，而不是返回错误。
func (r *Registry) SetDap(mgr DapSessionManager) {
	if r == nil {
		return
	}
	RegisterDebug(r)
	for _, tool := range debugTools(mgr) {
		r.tools[tool.Name()] = tool
	}
	r.wrapSchemas()
}
