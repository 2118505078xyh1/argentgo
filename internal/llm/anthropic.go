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
	Thinking     string          `json:"thinking,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
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
	if strings.TrimSpace(model) == "" {
		return Message{}, errors.New("anthropic: model is required")
	}
	payload, err := json.Marshal(c.buildRequest(model, messages, tools))
	if err != nil {
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
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("anthropic-version", c.version())
		if c.APIKey != "" {
			req.Header.Set("x-api-key", c.APIKey)
		}
		return c.httpClient().Do(req)
	})
	if err != nil {
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolAnthropicMessages, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		err := providerError(resp)
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolAnthropicMessages, model, err))
		return Message{}, err
	}

	reply, err := c.readStream(ctx, resp.Body, onText)
	if err != nil {
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolAnthropicMessages, model, err))
		return Message{}, err
	}
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
			if text := strings.TrimSpace(message.Content); text != "" {
				system = append(system, message.Content)
			}
		case "user":
			blocks := textBlocks(message.Content)
			if len(blocks) == 0 {
				continue
			}
			req.Messages = pushBlocks(req.Messages, "user", blocks)
		case "assistant":
			var blocks []anthropicBlock
			if c.Options.PreserveReasoning && message.Reasoning != "" {
				// 在助手轮次上，Thinking 块位于文本块之前。
				// 签名不属于中立 Message 契约，因此
				// 重放只携带文本；要求签名的提供方
				// 必须在网关层提供它。
				blocks = append(blocks, anthropicBlock{Type: "thinking", Thinking: message.Reasoning})
			}
			blocks = append(blocks, textBlocks(message.Content)...)
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
	// 工具增量 hook 为可选接缝：未安装时只是一次廉价的 context 查询，
	// 后续每处上报都以 if hook != nil 守卫，不分配、不改变任何 wire 行为。
	hook := HookFromContext(ctx)
	reader := newSSEReader(body)

	var (
		text      strings.Builder
		reasoning strings.Builder
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
		arguments = integrity.toolArguments(arguments)
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
			next, ok, err := reader.next()
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Message{}, ctxErr
				}
				return Message{}, streamError("read stream: %v", RedactError(err))
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
					if onText != nil {
						onText(block.Text)
					}
				}
			case "thinking":
				reasoning.WriteString(block.Thinking)
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
					if onText != nil {
						onText(delta.Text)
					}
				}
			case "thinking_delta":
				reasoning.WriteString(delta.Thinking)
			case "signature_delta":
				// 签名不属于中立 Message 契约；
				// 思考文本在这里仅用于转录。
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

	return Message{
		Role:         "assistant",
		Content:      text.String(),
		ToolCalls:    calls,
		Reasoning:    reasoning.String(),
		Usage:        usage,
		FinishReason: finishReason,
	}, nil
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
