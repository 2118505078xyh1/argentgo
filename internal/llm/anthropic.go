package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAnthropicMaxTokens 在 AnthropicClient.MaxTokens 未设置时使用。
// Messages API 要求每个请求都带 max_tokens。
const DefaultAnthropicMaxTokens = 8192

// AnthropicVersion 是在 anthropic-version 中发送的线协议版本。
const AnthropicVersion = "2023-06-01"

// AnthropicClient 流式处理 Anthropic Messages 协议。
//
// 工具调用以 tool_use 内容块到达，其参数由
// input_json_delta 片段组装；工具结果在下一个请求中作为
// user 消息里的 tool_result 块发回。
type AnthropicClient struct {
	BaseURL   string
	APIKey    string
	Version   string
	MaxTokens int
	HTTP      *http.Client
	Retry     RetryPolicy
	Options   Options
	// IdleTimeout 限制流事件之间的静默时长。一个已打开的连接可能
	// 停止传送数据却从不报错；设置正的 IdleTimeout 后，读取会作为可重试的
	// 流错误失败，而不是一直挂起直到调用方的 context 过期。
	// 零值（默认）保持同步读取，不增加定时器或 goroutine。
	IdleTimeout time.Duration
}

// NewAnthropic 返回面向 baseURL（通常为 ".../v1"）的 Messages 客户端。
func NewAnthropic(baseURL, apiKey string) *AnthropicClient {
	return &AnthropicClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Version: AnthropicVersion,
		HTTP:    &http.Client{},
	}
}

var _ Chatter = (*AnthropicClient)(nil)
var _ PromptCacheCapable = (*AnthropicClient)(nil)

// PromptCacheCapability 报告是否发出稳定前缀标记。
func (c *AnthropicClient) PromptCacheCapability() CacheCapability {
	return CacheCapability{
		Supported: c.Options.cacheEnabled(ProtocolAnthropicMessages, c.BaseURL),
		Protocol:  ProtocolAnthropicMessages,
		Local:     isLocalEndpoint(c.BaseURL),
	}
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    any                `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
	// Thinking 在扩展思考预算可用时携带启用配置。
	// 默认省略，使请求字节与早期构建保持一致。
	Thinking *anthropicThinking `json:"thinking,omitempty"`
}

// anthropicThinking 是 Anthropic 扩展思考的启用配置。
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int64  `json:"budget_tokens"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// 启用 Options.PreserveReasoning 时，Thinking 在 "thinking" 块上携带
	// 重放的推理内容。否则省略，使请求
	// 字节与早期构建完全一致。
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	// Source 只在 image 块上设置，承载 base64 图片负载。
	// 对纯文本与工具块保持 nil，配合 omitempty 使现有请求字节逐字节不变。
	Source       *anthropicImageSource `json:"source,omitempty"`
	CacheControl *CacheControl         `json:"cache_control,omitempty"`
}

// anthropicImageSource 是 Anthropic Messages 原生 image 块的 source 负载，
// 对应 {"type":"base64","media_type":"...","data":"..."}。
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
}

// Chat 流式获取一个助手轮次，并组装文本、思考和工具调用。
func (c *AnthropicClient) Chat(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
	onText func(string),
) (Message, error) {
	start := time.Now() // 请求级 trace 计时；未安装 sink 时 recordTrace 立即返回
	if strings.TrimSpace(model) == "" {
		err := errors.New("anthropic: model is required")
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		return Message{}, err
	}
	payload, err := json.Marshal(c.buildRequest(model, messages, tools))
	if err != nil {
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		return Message{}, fmt.Errorf("encode request: %w", err)
	}
	recordRequestSnapshot(ctx, ProtocolAnthropicMessages, model, payload)
	return c.sendAnthropicStream(ctx, model, payload, onText)
}

// sendAnthropicStream 发送已序列化的流式请求体并读取回复。Chat 与 ChatRaw 共用。
func (c *AnthropicClient) sendAnthropicStream(ctx context.Context, model string, payload []byte, onText func(string)) (Message, error) {
	start := time.Now()

	policy := c.Retry
	if policy.IsZero() {
		policy = DefaultRetryPolicy()
	}
	if policy.OnRetry == nil && c.Options.AttemptHook != nil {
		hook := c.Options.AttemptHook
		policy.OnRetry = func(attempt RetryAttempt) {
			emitAttempt(hook, AttemptRecord{
				Kind:       AttemptRetry,
				Protocol:   ProtocolAnthropicMessages,
				Model:      model,
				Attempt:    attempt.Attempt,
				StatusCode: attempt.StatusCode,
				ErrorClass: attempt.Cause,
			})
		}
	}
	resp, err := policy.Do(ctx, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("anthropic-version", c.version())
		if c.APIKey != "" {
			req.Header.Set("x-api-key", c.APIKey)
		}
		ApplyExtraHeaders(req) // 可选覆盖/追加请求头；默认无注入时 no-op
		return c.httpClient().Do(req)
	})
	if err != nil {
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolAnthropicMessages, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		err := providerError(resp)
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolAnthropicMessages, model, err))
		return Message{}, err
	}

	reply, err := c.readStream(ctx, resp.Body, onText)
	if err != nil {
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolAnthropicMessages, model, err))
		return Message{}, err
	}
	recordTrace(ProtocolAnthropicMessages, model, reply.Usage, start, nil, "ok")
	emitAttempt(c.Options.AttemptHook, AttemptRecord{Kind: AttemptSuccess, Protocol: ProtocolAnthropicMessages, Model: model})
	return reply, nil
}

// ChatRaw 实现 RawChatter：直接发送已序列化的请求体（来自快照），跳过构造与快照写入。
func (c *AnthropicClient) ChatRaw(ctx context.Context, model string, body []byte, onText func(string)) (Message, error) {
	return c.sendAnthropicStream(ctx, model, body, onText)
}

// ChatOnce 以非流式模式获取一个助手轮次，供流中断后的回退路径使用。
// 它复用 buildRequest 生成与 Chat 完全一致的请求，只把 stream 置为 false，
// 并把单个 JSON 响应文档映射为 Message。鉴权、重试、trace 与尝试上报
// 路径均与 Chat 相同；默认流式 Chat 的线协议字节不受影响。
func (c *AnthropicClient) ChatOnce(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
) (Message, error) {
	start := time.Now()
	if strings.TrimSpace(model) == "" {
		err := errors.New("anthropic: model is required")
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		return Message{}, err
	}
	req := c.buildRequest(model, messages, tools)
	req.Stream = false
	payload, err := json.Marshal(req)
	if err != nil {
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		return Message{}, fmt.Errorf("encode request: %w", err)
	}

	policy := c.Retry
	if policy.IsZero() {
		policy = DefaultRetryPolicy()
	}
	if policy.OnRetry == nil && c.Options.AttemptHook != nil {
		hook := c.Options.AttemptHook
		policy.OnRetry = func(attempt RetryAttempt) {
			emitAttempt(hook, AttemptRecord{
				Kind:       AttemptRetry,
				Protocol:   ProtocolAnthropicMessages,
				Model:      model,
				Attempt:    attempt.Attempt,
				StatusCode: attempt.StatusCode,
				ErrorClass: attempt.Cause,
			})
		}
	}
	resp, err := policy.Do(ctx, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("anthropic-version", c.version())
		if c.APIKey != "" {
			req.Header.Set("x-api-key", c.APIKey)
		}
		ApplyExtraHeaders(req)
		return c.httpClient().Do(req)
	})
	if err != nil {
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolAnthropicMessages, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		err := providerError(resp)
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolAnthropicMessages, model, err))
		return Message{}, err
	}

	reply, err := decodeAnthropicOnce(resp.Body)
	if err != nil {
		recordTrace(ProtocolAnthropicMessages, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolAnthropicMessages, model, err))
		return Message{}, err
	}
	recordTrace(ProtocolAnthropicMessages, model, reply.Usage, start, nil, "ok")
	emitAttempt(c.Options.AttemptHook, AttemptRecord{Kind: AttemptSuccess, Protocol: ProtocolAnthropicMessages, Model: model})
	return reply, nil
}

func (c *AnthropicClient) endpoint() string {
	base := strings.TrimRight(c.BaseURL, "/")
	switch {
	case base == "":
		return "https://api.anthropic.com/v1/messages"
	case strings.HasSuffix(base, "/messages"):
		return base
	case strings.HasSuffix(base, "/v1"):
		return base + "/messages"
	default:
		return base + "/v1/messages"
	}
}

func (c *AnthropicClient) version() string {
	if c.Version == "" {
		return AnthropicVersion
	}
	return c.Version
}

func (c *AnthropicClient) maxTokens() int {
	if c.MaxTokens <= 0 {
		return DefaultAnthropicMaxTokens
	}
	return c.MaxTokens
}

// thinkingBudget 把 Options.Thinking.BudgetTokens 钳制到 [1024, maxTokens-1]。
// 未提供预算、预算非正，或 maxTokens 太小（maxTokens-1 < 1024）时返回 0，
// 表示本次请求不发送思考配置。Anthropic 要求 budget_tokens 小于 max_tokens，
// 且扩展思考的最小预算为 1024。
func (c *AnthropicClient) thinkingBudget() int64 {
	if c.Options.Thinking == nil || c.Options.Thinking.BudgetTokens <= 0 {
		return 0
	}
	maxTokens := c.maxTokens()
	if maxTokens-1 < 1024 {
		return 0
	}
	budget := c.Options.Thinking.BudgetTokens
	if budget < 1024 {
		budget = 1024
	}
	if limit := int64(maxTokens - 1); budget > limit {
		budget = limit
	}
	return budget
}

func (c *AnthropicClient) httpClient() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

// buildRequest 把中立消息转换为 Messages 线协议块。系统
// 提示被提升出来；相邻的同角色轮次会合并，这正是
// 把一批 tool_result 块保留在同一条 user 消息中的原因。
func (c *AnthropicClient) buildRequest(model string, messages []Message, tools []ToolSpec) anthropicRequest {
	req := anthropicRequest{
		Model:     model,
		MaxTokens: c.maxTokens(),
		Stream:    true,
	}

	var system []string
	for _, message := range messages {
		switch message.Role {
		case "system":
			// 带图消息在本协议上退化为占位文本，避免静默丢弃整条消息。
			text := messageTextForProtocol(message)
			if strings.TrimSpace(text) != "" {
				system = append(system, text)
			}
		case "user":
			// 带图 user 消息映射为 Anthropic 原生 content block：
			// 文本 part 变成 text 块，每个 image_url part 变成 image 块。
			// 解析失败或不含有效图片时回退到占位文本，绝不报错、也不
			// 把无法解析的数据发给提供方。
			var blocks []anthropicBlock
			if len(message.ContentParts) > 0 {
				text, images, ok := decodeContentPartsForImageInput(message.ContentParts)
				if ok && len(images) > 0 {
					blocks = append(blocks, textBlocks(text)...)
					for _, img := range images {
						blocks = append(blocks, anthropicBlock{
							Type: "image",
							Source: &anthropicImageSource{
								Type:      "base64",
								MediaType: img.MediaType,
								Data:      img.Data,
							},
						})
					}
				}
			}
			if len(blocks) == 0 {
				blocks = textBlocks(messageTextForProtocol(message))
			}
			if len(blocks) == 0 {
				continue
			}
			req.Messages = pushBlocks(req.Messages, "user", blocks)
		case "assistant":
			var blocks []anthropicBlock
			if c.Options.PreserveReasoning && message.Reasoning != "" && message.ReasoningSignature != "" {
				// 在助手轮次上，Thinking 块位于文本块之前。
				// 要求签名的提供方必须同时携带文本与签名；缺签名时
				// 降级为不发送思考块，避免被提供方以 400 拒绝。
				blocks = append(blocks, anthropicBlock{Type: "thinking", Thinking: message.Reasoning, Signature: message.ReasoningSignature})
			}
			blocks = append(blocks, textBlocks(messageTextForProtocol(message))...)
			for _, call := range message.ToolCalls {
				blocks = append(blocks, toolUseBlock(call))
			}
			if len(blocks) == 0 {
				continue
			}
			req.Messages = pushBlocks(req.Messages, "assistant", blocks)
		case "tool":
			if strings.TrimSpace(message.ToolCallID) == "" {
				continue
			}
			blocks := []anthropicBlock{{
				Type:      "tool_result",
				ToolUseID: message.ToolCallID,
				Content:   message.Content,
			}}
			req.Messages = pushBlocks(req.Messages, "user", blocks)
		}
	}

	if len(req.Messages) == 0 {
		req.Messages = pushBlocks(req.Messages, "user", textBlocks("Continue."))
	}

	cacheEnabled := c.Options.cacheEnabled(ProtocolAnthropicMessages, c.BaseURL)
	if len(system) > 0 {
		content := strings.Join(system, "\n\n")
		if cacheEnabled {
			// 系统提示和工具 schema 构成稳定前缀；
			// 会话尾部在下面有自己递增的断点。
			req.System = []anthropicBlock{{
				Type:         "text",
				Text:         content,
				CacheControl: c.Options.cacheControl(),
			}}
		} else {
			req.System = content
		}
	}
	for _, tool := range tools {
		schema := tool.Function.Parameters
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		req.Tools = append(req.Tools, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: schema,
		})
	}
	if cacheEnabled && len(req.Tools) > 0 {
		// 工具前缀末尾的一个断点覆盖所有 schema。
		req.Tools[len(req.Tools)-1].CacheControl = c.Options.cacheControl()
	}
	if cacheEnabled {
		// 会话尾部断点：把缓存锚定在最后一个工具
		// 结果上，使缓存前缀在每批工具调用完成后推进。
		// 工具轮次被合并到 user 消息中，因此标记
		// 属于该内容块，而非消息本身。
		if block := lastToolResultBlock(req.Messages); block != nil {
			block.CacheControl = c.Options.normalizedCacheControl()
		}
	}
	// 只有钳制后的预算大于零才启用扩展思考。
	if budget := c.thinkingBudget(); budget > 0 {
		req.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
	}
	return req
}

// lastToolResultBlock 返回线协议消息中最后一个 tool_result 内容块，
// 或当请求不含工具结果时返回 nil。从末尾扫描
// 只在最新尾部保留一个断点。
func lastToolResultBlock(messages []anthropicMessage) *anthropicBlock {
	for i := len(messages) - 1; i >= 0; i-- {
		content := messages[i].Content
		for j := len(content) - 1; j >= 0; j-- {
			if content[j].Type == "tool_result" {
				return &content[j]
			}
		}
	}
	return nil
}

func textBlocks(text string) []anthropicBlock {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []anthropicBlock{{Type: "text", Text: text}}
}

func toolUseBlock(call ToolCall) anthropicBlock {
	arguments := strings.TrimSpace(call.Function.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if !json.Valid([]byte(arguments)) {
		arguments = "{}"
	}
	return anthropicBlock{
		Type:  "tool_use",
		ID:    call.ID,
		Name:  call.Function.Name,
		Input: json.RawMessage(arguments),
	}
}

func pushBlocks(messages []anthropicMessage, role string, blocks []anthropicBlock) []anthropicMessage {
	if len(blocks) == 0 {
		return messages
	}
	if n := len(messages); n > 0 && messages[n-1].Role == role {
		messages[n-1].Content = append(messages[n-1].Content, blocks...)
		return messages
	}
	return append(messages, anthropicMessage{Role: role, Content: blocks})
}

type anthropicUsage struct {
	InputTokens int `json:"input_tokens"`
	// 缓存计数用指针表示，以便把 message_delta 中显式的零与
	// 省略的字段区分开。原生 Anthropic 流在 message_delta 中省略它们，
	// 但在那里报告最终缓存总计的网关
	// 可能在非零 message_start 之后合法地报告零；把两者
	// 都当作零值会保留陈旧的 message_start 计数。
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
}

func intOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicPartial struct {
	id        string
	name      string
	arguments strings.Builder
	start     string
}

func (c *AnthropicClient) readStream(ctx context.Context, body io.Reader, onText func(string)) (Message, error) {
	// 流式时间点打点为 opt-in：默认关闭时不包装 onText、不调用 time.Now，
	// 也不改写 Usage，因此既有行为的字节与内存表现完全一致。
	timingOn := streamTimingOn()
	var timing streamTiming
	if timingOn {
		timing.requestMS = time.Now().UnixMilli()
		onText = timing.wrapOnText(onText)
	}
	// 工具增量 hook 为可选接缝：未安装时只是一次廉价的 context 查询，
	// 后续每处上报都以 if hook != nil 守卫，不分配、不改变任何 wire 行为。
	hook := HookFromContext(ctx)
	idleReader := newSSEIdleReader(newSSEReader(body), c.IdleTimeout)
	defer idleReader.stop()

	var (
		text      strings.Builder
		reasoning strings.Builder
		// signature 累积思考块的签名：content_block_start 上的初始签名
		// 加上后续 signature_delta 分片，供下游在后续轮次中重放思考块。
		signature strings.Builder
		usage     *Usage
		// baseInput 是 message_start 的原始 input_tokens（不含缓存）。
		// message_delta 携带权威的缓存计数，因此 PromptTokens
		// 从这个基数重建，而不是把两个快照相加。
		baseInput int
		// deltaInput 记住 message_delta 中出现的最大 input_tokens。
		// 透传网关（如 LiteLLM/OpenRouter）会把 message_start 的
		// usage 清零，并在这里报告真实的提示总成本。
		deltaInput   int
		finishReason string
		integrity    streamIntegrity
		partials     = make(map[int]*anthropicPartial)
	)
	integrity.strict = c.Options.StrictStreamTerminator
	// 已完成的 tool_use 块按其内容块索引停放，
	// 而不是在 content_block_stop 时追加：网关可能
	// 以与 content_block_start 宣布顺序不同的顺序投递停止通知。
	// 最终组装按 order（宣布顺序）遍历，因此两个
	// 并行调用无论各块何时完成，
	// 都保持提供方的顺序。
	var (
		order     []int
		completed = make(map[int]ToolCall)
	)
	var pending []string

	push := func(index int) {
		partial, ok := partials[index]
		if !ok {
			return
		}
		delete(partials, index)
		if strings.TrimSpace(partial.name) == "" {
			return
		}
		arguments := partial.arguments.String()
		if strings.TrimSpace(arguments) == "" {
			arguments = partial.start
		}
		arguments, raw := integrity.toolArgumentsWithRaw(arguments)
		// 这里保持缺失的提供方 id 为空。回退 id 在
		// 最终组装时分配，届时回复中每个提供方 id 都已知，因此
		// 生成的 "toolu_N" 绝不会与另一块上的真实 id 重复。
		completed[index] = ToolCall{
			ID:   partial.id,
			Type: "function",
			Function: ToolCallFunction{
				Name:      partial.name,
				Arguments: arguments,
			},
			InvalidArguments: raw,
		}
	}

streamLoop:
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		var data string
		if len(pending) > 0 {
			data, pending = pending[0], pending[1:]
		} else {
			next, ok, err := idleReader.next(ctx)
			if err != nil {
				return Message{}, err
			}
			if !ok {
				break
			}
			pending = splitSSEData(next)
			data, pending = pending[0], pending[1:]
		}
		// 空的 data: 行是合法的 SSE 保活，不是 JSON 事件。
		// 跳过它可避免代理的心跳导致回复失败。
		if isEmptySSEData(data) {
			continue
		}
		if data == "[DONE]" {
			integrity.terminated = true
			break
		}

		var event anthropicEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Message{}, streamError("decode stream event: %v", RedactError(err))
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				baseInput = event.Message.Usage.InputTokens
				usage = anthropicPromptUsage(event.Message.Usage)
			}
		case "content_block_start":
			block := event.ContentBlock
			if block == nil {
				continue
			}
			switch block.Type {
			case "text":
				if block.Text != "" {
					text.WriteString(block.Text)
					integrity.sawAnyText = true
					if onText != nil {
						onText(block.Text)
					}
				}
			case "thinking":
				reasoning.WriteString(block.Thinking)
				// 部分网关在块开始时即附带完整签名。
				if block.Signature != "" {
					signature.WriteString(block.Signature)
				}
			case "tool_use":
				partial := &anthropicPartial{id: block.ID, name: block.Name}
				if raw := strings.TrimSpace(string(block.Input)); raw != "" && raw != "{}" {
					partial.start = raw
				}
				if _, exists := partials[event.Index]; !exists {
					order = append(order, event.Index)
				}
				partials[event.Index] = partial
				if hook != nil {
					// 元数据出现点：先发一枚空参数片，携带 id/name。
					hook(ToolCallDelta{Index: event.Index, ID: block.ID, Name: block.Name})
				}
			}
		case "content_block_delta":
			delta := event.Delta
			if delta == nil {
				continue
			}
			switch delta.Type {
			case "text_delta":
				if delta.Text != "" {
					text.WriteString(delta.Text)
					integrity.sawAnyText = true
					if onText != nil {
						onText(delta.Text)
					}
				}
			case "thinking_delta":
				reasoning.WriteString(delta.Thinking)
			case "signature_delta":
				// 签名的分片在流末尾拼接，用于重放时校验思考块。
				signature.WriteString(delta.Signature)
			case "input_json_delta":
				if partial := partials[event.Index]; partial != nil {
					partial.arguments.WriteString(delta.PartialJSON)
					if hook != nil {
						// 上报本片的原始 JSON 分片，并附上已解析的元数据。
						hook(ToolCallDelta{
							Index:     event.Index,
							ID:        partial.id,
							Name:      partial.name,
							Arguments: delta.PartialJSON,
						})
					}
				}
			}
		case "content_block_stop":
			push(event.Index)
		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				finishReason = event.Delta.StopReason
			}
			if event.Usage != nil {
				if usage == nil {
					usage = &Usage{}
				}
				if event.Usage.OutputTokens > 0 {
					usage.CompletionTokens = event.Usage.OutputTokens
				}
				// message_delta 中的缓存计数是最终的累计值，不是
				// 增量。应赋值（绝不累加），以免 message_start 中也出现的
				// 缓存计数被重复计算。nil 指针表示该字段
				// 被省略（保留 message_start 的值）；存在的零是
				// 权威的最终计数，必须清除任何陈旧值。
				if event.Usage.CacheReadInputTokens != nil {
					usage.CachedTokens = *event.Usage.CacheReadInputTokens
				}
				if event.Usage.CacheCreationInputTokens != nil {
					usage.CacheWriteTokens = *event.Usage.CacheCreationInputTokens
				}
				// PromptTokens 是完整的提示成本（新增 + 缓存）。从
				// message_start 的原始输入加上权威的增量缓存
				// 计数重建它；否则 CachedTokens 可能超过 PromptTokens，导致预算
				// 核算低估。透传网关可能把
				// message_start 清零，并把真实总计作为 message_delta 的
				// input_tokens 报告，因此取较大的总计：当它计入了 message_start
				// 从未报告的 token 时，该值具有权威性；
				// 而仅仅重复其原始输入的原生流
				// 不能覆盖缓存部分。
				if event.Usage.InputTokens > deltaInput {
					deltaInput = event.Usage.InputTokens
				}
				prompt := baseInput + usage.CachedTokens + usage.CacheWriteTokens
				if deltaInput > prompt {
					prompt = deltaInput
				}
				usage.PromptTokens = prompt
			}
		case "message_stop":
			integrity.terminated = true
			break streamLoop
		case "error":
			message := "unknown provider error"
			if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			}
			return Message{}, streamError("provider error: %s", RedactText(message))
		}
	}

	// 省略 content_block_stop 的网关仍会交回完整的调用。
	for _, index := range sortedIndices(partials) {
		push(index)
	}
	// 在生成任何回退 id 之前，先收集所有提供方 id，使生成的
	// "toolu_N" 绝不会与提供方在另一块上提供的 id 重复。
	// chat 客户端应用相同规则；这里的冲突会让
	// agent 因调用 id 重复而拒绝整个批次。
	used := make(map[string]struct{}, len(order))
	for _, index := range order {
		if call, ok := completed[index]; ok && call.ID != "" {
			used[call.ID] = struct{}{}
		}
	}
	// 按宣布顺序而非停止通知顺序组装。同一索引处重复的
	// start 可能把一个索引两次加入 order；每个调用
	// 只发出一次。
	calls := make([]ToolCall, 0, len(order))
	emitted := make(map[int]bool, len(order))
	for _, index := range order {
		if emitted[index] {
			continue
		}
		emitted[index] = true
		if call, ok := completed[index]; ok {
			if call.ID == "" {
				call.ID = uniqueToolCallID(used, "toolu_", index)
			}
			calls = append(calls, call)
		}
	}
	if err := integrity.eofError("message_stop"); err != nil {
		return Message{}, err
	}

	if timingOn {
		timing.stamp(&usage)
	}
	return Message{
		Role:               "assistant",
		Content:            text.String(),
		ToolCalls:          calls,
		Reasoning:          reasoning.String(),
		ReasoningSignature: signature.String(),
		Usage:              usage,
		FinishReason:       finishReason,
	}, nil
}

// anthropicOnceResponse 是 Messages API 非流式单次响应的最小解码结构。
type anthropicOnceResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      *anthropicUsage  `json:"usage"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// decodeAnthropicOnce 把单个 JSON 响应映射为助手轮次。它刻意不依赖
// readStream 的 SSE 状态机：工具参数在单次响应中已是完整 JSON，
// 因此不需要增量拼接或截断修复。
func decodeAnthropicOnce(body io.Reader) (Message, error) {
	var response anthropicOnceResponse
	raw, err := io.ReadAll(body)
	if err != nil {
		return Message{}, fmt.Errorf("read response: %w", err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return Message{}, fmt.Errorf("decode response: %s", RedactError(err))
	}
	if response.Error != nil {
		message := firstNonEmpty(response.Error.Message, "unknown provider error")
		return Message{}, fmt.Errorf("provider error: %s", RedactText(message))
	}

	var (
		text      strings.Builder
		reasoning strings.Builder
		signature strings.Builder
		calls     []ToolCall
		used      = make(map[string]struct{})
	)
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
			signature.WriteString(block.Signature)
		case "tool_use":
			call := ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      block.Name,
					Arguments: onceToolArguments(block.Input),
				},
			}
			if call.ID != "" {
				used[call.ID] = struct{}{}
			}
			calls = append(calls, call)
		}
	}
	// 先收集所有提供方 id，再为缺失者分配回退 id，
	// 使生成的 "toolu_N" 绝不与真实 id 重复。
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = uniqueToolCallID(used, "toolu_", i)
		}
	}
	if text.Len() == 0 && reasoning.Len() == 0 && len(calls) == 0 {
		return Message{}, errors.New("anthropic: empty content in non-stream response")
	}

	var usage *Usage
	if response.Usage != nil {
		usage = anthropicPromptUsage(response.Usage)
	}
	return Message{
		Role:               "assistant",
		Content:            text.String(),
		ToolCalls:          calls,
		Reasoning:          reasoning.String(),
		ReasoningSignature: signature.String(),
		Usage:              usage,
		FinishReason:       response.StopReason,
	}, nil
}

// onceToolArguments 把非流式工具输入规范化为 JSON 参数串。
func onceToolArguments(raw json.RawMessage) string {
	arguments := strings.TrimSpace(string(raw))
	if arguments == "" || !json.Valid([]byte(arguments)) {
		return "{}"
	}
	return arguments
}

func anthropicPromptUsage(usage *anthropicUsage) *Usage {
	read := intOrZero(usage.CacheReadInputTokens)
	write := intOrZero(usage.CacheCreationInputTokens)
	return &Usage{
		PromptTokens:     usage.InputTokens + write + read,
		CompletionTokens: usage.OutputTokens,
		CachedTokens:     read,
		CacheWriteTokens: write,
	}
}

func sortedIndices(partials map[int]*anthropicPartial) []int {
	indices := make([]int, 0, len(partials))
	for index := range partials {
		indices = append(indices, index)
	}
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && indices[j] < indices[j-1]; j-- {
			indices[j], indices[j-1] = indices[j-1], indices[j]
		}
	}
	return indices
}

// sseReader 把响应体转换为 Server-Sent Events 载荷。多行
// data 字段用换行符连接，注释和非 data 字段被
// 跳过，空行终止一个事件。
type sseReader struct {
	scanner *bufio.Scanner
	data    []string
}

func newSSEReader(r io.Reader) *sseReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &sseReader{scanner: scanner}
}

func (s *sseReader) next() (string, bool, error) {
	for s.scanner.Scan() {
		line := strings.TrimRight(s.scanner.Text(), "\r")
		if line == "" {
			if len(s.data) == 0 {
				continue
			}
			payload := strings.Join(s.data, "\n")
			s.data = s.data[:0]
			return payload, true, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok || field != "data" {
			continue
		}
		s.data = append(s.data, strings.TrimPrefix(value, " "))
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, err
	}
	if len(s.data) > 0 {
		payload := strings.Join(s.data, "\n")
		s.data = nil
		return payload, true, nil
	}
	return "", false, nil
}
