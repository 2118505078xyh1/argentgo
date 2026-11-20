package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// RunACP 提供 Agent Client Protocol 服务（基于 stdio 的 JSON-RPC 2.0，每行
// 一条消息），直到 in 到达 EOF 或 ctx 被取消。
//
// 支持的方法：initialize、authenticate、session/new、session/prompt、
// session/cancel、session/set_model、session/set_mode、session/request_permission。
// 流式助手文本作为 session/update 通知发送。
func RunACP(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	ctx, cancelAll := context.WithCancel(ctx)

	state := &acpState{
		cfg:      cfg,
		peer:     newPeer(out),
		sessions: make(map[string]bool),
	}

	var wg sync.WaitGroup
	defer func() {
		cancelAll()
		wg.Wait()
	}()

	results := scanLines(ctx, in)
	for {
		// 优先处理取消，使排队的行不会延迟退出。
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case res, ok := <-results:
			if !ok { // EOF
				return nil
			}
			if res.err != nil {
				if reportScanError(state.peer, res.err) {
					return res.err
				}
				continue
			}
			var req request
			if err := json.Unmarshal(res.line, &req); err != nil {
				state.peer.fail(json.RawMessage("null"), codeParseError, "parse error: "+err.Error())
				continue
			}
			if req.Method == "" {
				state.peer.fail(req.ID, codeInvalidRequest, "missing method")
				continue
			}
			state.handle(ctx, req, &wg)
		}
	}
}

type acpState struct {
	cfg  Config
	peer *peer

	mu           sync.Mutex
	sessionID    string
	sessions     map[string]bool
	cancelPrompt context.CancelFunc
	busy         atomic.Bool
}

func (s *acpState) handle(ctx context.Context, req request, wg *sync.WaitGroup) {
	switch req.Method {
	case "initialize":
		version := s.cfg.Version
		if version == "" {
			version = "dev"
		}
		s.peer.respond(req.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"promptCapabilities": map[string]any{
					"image":           true,
					"audio":           false,
					"embeddedContext": true,
				},
				"mcpCapabilities": map[string]any{"http": false, "sse": false},
				"loadSession":     false,
			},
			"authMethods": []any{},
			"agentInfo":   map[string]any{"name": "argentgo", "version": version},
		})
	case "authenticate":
		s.peer.respond(req.ID, map[string]any{})
	case "session/new":
		id := newSessionID()
		s.mu.Lock()
		s.sessions[id] = true
		s.sessionID = id
		s.mu.Unlock()
		s.peer.respond(req.ID, map[string]any{"sessionId": id})
	case "session/prompt":
		s.prompt(ctx, req, wg)
	case "session/cancel":
		s.cancel()
		s.peer.respond(req.ID, map[string]any{})
	case "session/set_model", "session/set_mode":
		s.peer.respond(req.ID, map[string]any{})
	case "session/request_permission":
		s.requestPermission(req)
	case "session/load":
		s.peer.fail(req.ID, codeMethodNotFound, "session/load is not supported; start a new session")
	default:
		s.peer.fail(req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}

// promptTerminal 是 ACP session/prompt 的结果。TurnEndReason 是附加的
// 且 omitempty：只读 stopReason 的客户端对每种结果看到的都是原始线协议
// 形态，因此这是协议表面的纯扩展。
type promptTerminal struct {
	StopReason    string        `json:"stopReason"`
	TurnEndReason TurnEndReason `json:"turnEndReason,omitempty"`
}

func (s *acpState) prompt(parent context.Context, req request, wg *sync.WaitGroup) {
	text := promptText(req.Params)
	if text == "" {
		s.peer.fail(req.ID, codeInvalidParams, "prompt has no text")
		return
	}
	if s.cfg.Runner == nil {
		s.peer.fail(req.ID, codeServerError, "no runner configured")
		return
	}

	s.mu.Lock()
	requested := sessionIDFromParams(req.Params)
	if requested == "" {
		// ACP 要求 sessionId，但对依赖旧的
		// "最近创建的会话"行为的客户端保持宽松。
		requested = s.sessionID
	}
	// 要针对本连接上创建的每一个会话校验，而不只是最新的那个。
	// 客户端可能打开多个会话并返回较早的一个；sessions 正是
	// session/new 为此维护的注册表。
	_, known := s.sessions[requested]
	s.mu.Unlock()
	if !known {
		s.peer.fail(req.ID, codeInvalidParams, "unknown session")
		return
	}
	if !s.busy.CompareAndSwap(false, true) {
		s.peer.fail(req.ID, codeInvalidRequest, "a prompt is already running")
		return
	}

	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancelPrompt = cancel
	s.mu.Unlock()

	id := req.ID
	session := requested
	wg.Add(1)
	go func() {
		defer wg.Done()

		result, err := s.cfg.Runner.Prompt(ctx, text, func(chunk string) {
			if chunk == "" {
				return
			}
			s.peer.notify("session/update", map[string]any{
				"sessionId": session,
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": chunk},
				},
			})
		})
		_ = result

		// 在下面调用 cancel() 之前捕获结果：一旦 cancel()
		// 运行，所有错误的 ctx.Err() 都是 context.Canceled，真正的
		// 失败会被误报为正常停止。
		cancelled := err != nil && isCancelled(ctx, err)
		reason := ClassifyTurnEnd(err, cancelled)

		// 在发布终态结果*之前*释放单提示槽位。结果的
		// 写入对客户端是可见的：一旦看到该结果就启动下一个提示的
		// 客户端，不得被延迟清理尚未到达的陈旧 busy 标志拒绝。
		// 在 busy 仍被持有时清除 cancelPrompt，
		// 使新提示永远看不到陈旧的取消句柄，
		// 然后释放 busy。
		s.mu.Lock()
		s.cancelPrompt = nil
		s.mu.Unlock()
		cancel()
		s.busy.Store(false)

		if err != nil {
			// 与 --rpc 和 --serve 相同的取消规则；线协议结果
			// 保持 ACP 客户端期望的 {"stopReason":"cancelled"}。
			// 新增的 turnEndReason 兼容 omitempty，因此这是附加的。
			if cancelled {
				s.peer.respond(id, promptTerminal{StopReason: "cancelled", TurnEndReason: reason})
				return
			}
			// 真正的失败仍是 JSON-RPC 错误：其 code (-32603) 已经
			// 把它与两种结果形态的结果区分开，且 code/message
			// 都未改变。唯一的增加是可选的
			// error.data 携带 "failure" 分类，使只消费
			// turnEndReason 的调用方不再需要从 code 推断它。
			s.peer.failWithData(id, codeInternalError, wireErrorText(err), failureTurnEndData(reason))
			return
		}
		s.peer.respond(id, promptTerminal{StopReason: "end_turn", TurnEndReason: reason})
	}()
}

func (s *acpState) cancel() {
	s.mu.Lock()
	cancel := s.cancelPrompt
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if s.cfg.Runner != nil {
		s.cfg.Runner.Abort()
	}
}

func sessionIDFromParams(raw json.RawMessage) string {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if len(raw) == 0 {
		return ""
	}
	_ = json.Unmarshal(raw, &params)
	return params.SessionID
}

func newSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return "sess_" + hex.EncodeToString(buf[:])
}

// ---- session/request_permission（ACP 协议骨架）----
//
// ACP 为工具调用定义了四种权限选择：允许/拒绝，每种
// 可一次或始终。argent-go 只实现协议表面：构造规范
// 选项集，并把客户端提供的 optionId 映射到结果。
// 这里有意不做交互式等待——把请求桥接到持久的
// security.PendingStore（并等待 approval.answer）是
// 上层的职责。在接线完成之前，任何不完全匹配
// 四个选项之一的输入都会解析为 "cancelled"，因此该方法永远不会
// 意外批准工具调用（fail closed，最小权限）。

// PermissionOptionKind 是四种 ACP 权限决策语义之一。
type PermissionOptionKind string

const (
	PermissionOptionAllowOnce    PermissionOptionKind = "allow_once"
	PermissionOptionAllowAlways  PermissionOptionKind = "allow_always"
	PermissionOptionRejectOnce   PermissionOptionKind = "reject_once"
	PermissionOptionRejectAlways PermissionOptionKind = "reject_always"
)

// PermissionOption 是提供给 ACP 客户端的一个可选项。
type PermissionOption struct {
	OptionID string               `json:"optionId"`
	Kind     PermissionOptionKind `json:"kind"`
	Name     string               `json:"name"`
}

// RequestPermissionParams 是入站的 ACP 请求。SessionID 和 ToolCallID
// 标识正在被决策的工具调用；OptionID 是客户端回显的选项，
// 也是唯一能产生 "selected" 结果的字段。Options
// 对应 ACP 请求形态，但永不被信任：permissionOptions() 是
// 什么可被选择的唯一事实来源。
type RequestPermissionParams struct {
	SessionID  string             `json:"sessionId"`
	ToolCallID string             `json:"toolCallId"`
	OptionID   string             `json:"optionId,omitempty"`
	Options    []PermissionOption `json:"options,omitempty"`
}

// RequestPermissionOutcome 是 ACP 的 "outcome" 对象：要么带所选
// optionId 的 "selected"，要么不带 optionId 的 "cancelled"。
type RequestPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// RequestPermissionResult 是 ACP session/request_permission 的响应。
type RequestPermissionResult struct {
	Outcome RequestPermissionOutcome `json:"outcome"`
}

// permissionOptions 返回四个规范的 ACP 权限选项。
// optionId 同时充当 kind 字符串，因此校验无需生成 ID。
func permissionOptions() []PermissionOption {
	return []PermissionOption{
		{OptionID: string(PermissionOptionAllowOnce), Kind: PermissionOptionAllowOnce, Name: "Allow once"},
		{OptionID: string(PermissionOptionAllowAlways), Kind: PermissionOptionAllowAlways, Name: "Always allow"},
		{OptionID: string(PermissionOptionRejectOnce), Kind: PermissionOptionRejectOnce, Name: "Reject once"},
		{OptionID: string(PermissionOptionRejectAlways), Kind: PermissionOptionRejectAlways, Name: "Always reject"},
	}
}

// permissionOutcome 把客户端提供的 optionId 映射到规范集合。
// 只接受精确匹配；空、未知或大小写错误的选项
// 会产生 "cancelled"（fail closed，最小权限）。
func permissionOutcome(optionID string) RequestPermissionOutcome {
	for _, opt := range permissionOptions() {
		if opt.OptionID == optionID {
			return RequestPermissionOutcome{Outcome: "selected", OptionID: opt.OptionID}
		}
	}
	return RequestPermissionOutcome{Outcome: "cancelled"}
}

// requestPermission 是 session/request_permission 的协议骨架。
// 格式错误的 params 是 JSON-RPC 错误；任何不带
// 可识别选项的 params（空、缺失或未知）都返回 "cancelled" 结果。
func (s *acpState) requestPermission(req request) {
	var params RequestPermissionParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.peer.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	s.peer.respond(req.ID, RequestPermissionResult{Outcome: permissionOutcome(params.OptionID)})
}
