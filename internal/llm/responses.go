package llm

import (
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

// DefaultResponsesMaxOutputTokens 在 ResponsesClient.MaxOutputTokens
// 未设置时使用。
const DefaultResponsesMaxOutputTokens = 8192

// ResponsesClient 流式处理 OpenAI Responses 协议（/responses）。
//
// 它使用 item 而非 message：user/assistant 轮次保留其角色，
// 函数调用变成 function_call item，工具输出作为以 call_id 为键的
// function_call_output item 返回。
type ResponsesClient struct {
	BaseURL         string
	APIKey          string
	MaxOutputTokens int
	HTTP            *http.Client
	Retry           RetryPolicy
	Options         Options
	// IdleTimeout 限制流事件之间的静默时长。一个已打开的连接可能
	// 停止传送数据却从不报错；设置正的 IdleTimeout 后，读取会作为可重试的
	// 流错误失败，而不是一直挂起直到调用方的 context 过期。
	// 零值（默认）保持同步读取，不增加定时器或 goroutine。
	IdleTimeout time.Duration
}

// NewResponses 返回面向 baseURL（通常为 ".../v1"）的 Responses 客户端。
func NewResponses(baseURL, apiKey string) *ResponsesClient {
	return &ResponsesClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{},
	}
}

var _ Chatter = (*ResponsesClient)(nil)
var _ PromptCacheCapable = (*ResponsesClient)(nil)

// PromptCacheCapability 报告是否发出稳定前缀的缓存键。
func (c *ResponsesClient) PromptCacheCapability() CacheCapability {
	return CacheCapability{
		Supported: c.Options.cacheEnabled(ProtocolResponses, c.BaseURL),
		Protocol:  ProtocolResponses,
		Local:     isLocalEndpoint(c.BaseURL),
	}
}

type responsesRequest struct {
	Model                string          `json:"model"`
	Instructions         string          `json:"instructions,omitempty"`
	Input                []any           `json:"input"`
	Tools                []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens      int             `json:"max_output_tokens"`
	Stream               bool            `json:"stream"`
	Store                bool            `json:"store"`
	PromptCacheKey       string          `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string          `json:"prompt_cache_retention,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responsesTextItem struct {
	// 文本消息项必须携带 type:"message"：Responses API 在 content 使用带类型的分段
	// （input_text / output_text）时依赖这个判别字段识别消息项，缺失会被服务端拒绝。
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content []responsesPart `json:"content"`
}

type responsesPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesCallItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// Chat 流式获取一个助手轮次，并组装文本、推理和调用。
func (c *ResponsesClient) Chat(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
	onText func(string),
) (Message, error) {
	start := time.Now() // 请求级 trace 计时；未安装 sink 时 recordTrace 立即返回
	if strings.TrimSpace(model) == "" {
		err := errors.New("responses: model is required")
		recordTrace(ProtocolResponses, model, nil, start, err, "")
		return Message{}, err
	}
	payload, err := json.Marshal(c.buildRequest(model, messages, tools))
	if err != nil {
		recordTrace(ProtocolResponses, model, nil, start, err, "")
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
				Protocol:   ProtocolResponses,
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
		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		ApplyExtraHeaders(req) // 可选覆盖/追加请求头；默认无注入时 no-op
		return c.httpClient().Do(req)
	})
	if err != nil {
		recordTrace(ProtocolResponses, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolResponses, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		err := providerError(resp)
		recordTrace(ProtocolResponses, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolResponses, model, err))
		return Message{}, err
	}

	reply, err := c.readStream(ctx, resp.Body, onText)
	if err != nil {
		recordTrace(ProtocolResponses, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolResponses, model, err))
		return Message{}, err
	}
	recordTrace(ProtocolResponses, model, reply.Usage, start, nil, "ok")
	emitAttempt(c.Options.AttemptHook, AttemptRecord{Kind: AttemptSuccess, Protocol: ProtocolResponses, Model: model})
	return reply, nil
}

func (c *ResponsesClient) endpoint() string {
	base := strings.TrimRight(c.BaseURL, "/")
	switch {
	case base == "":
		return "https://api.openai.com/v1/responses"
	case strings.HasSuffix(base, "/responses"):
		return base
	case strings.HasSuffix(base, "/v1"):
		return base + "/responses"
	default:
		return base + "/v1/responses"
	}
}

func (c *ResponsesClient) maxOutputTokens() int {
	if c.MaxOutputTokens <= 0 {
		return DefaultResponsesMaxOutputTokens
	}
	return c.MaxOutputTokens
}

func (c *ResponsesClient) httpClient() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

func (c *ResponsesClient) buildRequest(model string, messages []Message, tools []ToolSpec) responsesRequest {
	req := responsesRequest{
		Model:           model,
		MaxOutputTokens: c.maxOutputTokens(),
		Stream:          true,
		Store:           false,
	}

	var instructions []string
	for _, message := range messages {
		// 带图消息在本协议上退化为占位文本，避免静默丢弃整条消息。
		text := messageTextForProtocol(message)
		switch message.Role {
		case "system":
			if strings.TrimSpace(text) != "" {
				instructions = append(instructions, text)
			}
		case "user":
			if strings.TrimSpace(text) == "" {
				continue
			}
			req.Input = append(req.Input, responsesTextItem{
				Type:    "message",
				Role:    "user",
				Content: []responsesPart{{Type: "input_text", Text: text}},
			})
		case "assistant":
			if strings.TrimSpace(text) != "" {
				req.Input = append(req.Input, responsesTextItem{
					Type:    "message",
					Role:    "assistant",
					Content: []responsesPart{{Type: "output_text", Text: text}},
				})
			}
			for _, call := range message.ToolCalls {
				arguments := strings.TrimSpace(call.Function.Arguments)
				if arguments == "" || !json.Valid([]byte(arguments)) {
					arguments = "{}"
				}
				req.Input = append(req.Input, responsesCallItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Function.Name,
					Arguments: arguments,
				})
			}
		case "tool":
			if strings.TrimSpace(message.ToolCallID) == "" {
				continue
			}
			req.Input = append(req.Input, responsesOutputItem{
				Type:   "function_call_output",
				CallID: message.ToolCallID,
				Output: message.Content,
			})
		}
	}

	if len(req.Input) == 0 {
		req.Input = append(req.Input, responsesTextItem{
			Type:    "message",
			Role:    "user",
			Content: []responsesPart{{Type: "input_text", Text: "Continue."}},
		})
	}
	if len(instructions) > 0 {
		req.Instructions = strings.Join(instructions, "\n\n")
	}
	for _, tool := range tools {
		parameters := tool.Function.Parameters
		if len(bytes.TrimSpace(parameters)) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		req.Tools = append(req.Tools, responsesTool{
			Type:        "function",
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  parameters,
		})
	}
	if c.Options.cacheEnabled(ProtocolResponses, c.BaseURL) {
		toolJSON, _ := json.Marshal(req.Tools)
		req.PromptCacheKey = c.Options.PromptCacheKey
		if req.PromptCacheKey == "" {
			req.PromptCacheKey = deriveCacheKey(req.Instructions, string(toolJSON))
		}
		req.PromptCacheRetention = c.Options.CacheRetention
	}
	return req
}

type responsesEvent struct {
	Type        string `json:"type"`
	Delta       string `json:"delta"`
	OutputIndex int    `json:"output_index"`
	Message     string `json:"message"`
	// Arguments 是 response.function_call_arguments.done 携带的完整参数字符串。
	// 它是该次调用参数的权威来源：网关可能不发送增量、只发送这个事件，
	// 或增量在传输中丢失，此时只能靠它还原出正确的工具参数。
	Arguments string `json:"arguments"`
	// ItemID 是该事件对应的输出项 id，在网关省略 call_id 时作为 id 兜底。
	ItemID string `json:"item_id"`
	// Name 是部分版本在参数完成事件中附带的函数名。
	Name string `json:"name"`
	Item *struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response *struct {
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			// CacheWriteTokens 在部分网关上直接放在 usage 顶层；它们的
			// 写入量会被下游成本核算读取，遗漏会悄悄低估开销。
			CacheWriteTokens   int `json:"cache_write_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
				// CacheWriteTokens 是网关在细分里报告的缓存写入量，
				// 与顶层的同名字段二选一（细分优先）。
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		Error *responsesError `json:"error"`
	} `json:"response"`
	Error *responsesError `json:"error"`
}

type responsesError struct {
	Message string `json:"message"`
}

type responsesPartial struct {
	id        string
	name      string
	arguments strings.Builder
}

func (c *ResponsesClient) readStream(ctx context.Context, body io.Reader, onText func(string)) (Message, error) {
	// 工具增量 hook 为可选接缝：未安装时只是一次廉价的 context 查询，
	// 后续每处上报都以 if hook != nil 守卫，不分配、不改变任何 wire 行为。
	hook := HookFromContext(ctx)
	idleReader := newSSEIdleReader(newSSEReader(body), c.IdleTimeout)
	defer idleReader.stop()

	var (
		text         strings.Builder
		reasoning    strings.Builder
		usage        *Usage
		finishReason string
		order        []int
		integrity    streamIntegrity
		partials     = make(map[int]*responsesPartial)
		// 已完成的调用按其 output index 停放，而不是
		// 立即追加。output_item.done 事件的到达顺序可能与
		// output_item.added 宣布项的顺序不同（网关会批处理
		// 并重排通知）；在 done 时发出会悄悄交换两个
		// 并行调用。下面的最终组装按 order（即提供方的宣布顺序）遍历，
		// 因此调用列表始终与该顺序一致。
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
		arguments := integrity.toolArguments(partial.arguments.String())
		// 缺失的提供方 id 在最终组装前保持为空，届时
		// 可以根据回复中每个提供方 id 选择一个回退值。
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

		var event responsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Message{}, streamError("decode stream event: %v", RedactError(err))
		}

		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				text.WriteString(event.Delta)
				if onText != nil {
					onText(event.Delta)
				}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(event.Delta)
		case "response.output_item.added":
			if event.Item == nil || event.Item.Type != "function_call" {
				continue
			}
			partial := &responsesPartial{
				id:   firstNonEmpty(event.Item.CallID, event.Item.ID),
				name: event.Item.Name,
			}
			partial.arguments.WriteString(event.Item.Arguments)
			if hook != nil {
				// 上报新增项携带的元数据与可能的初始参数片。
				hook(ToolCallDelta{
					Index:     event.OutputIndex,
					ID:        partial.id,
					Name:      partial.name,
					Arguments: event.Item.Arguments,
				})
			}
			if _, exists := partials[event.OutputIndex]; !exists {
				order = append(order, event.OutputIndex)
			}
			partials[event.OutputIndex] = partial
		case "response.function_call_arguments.delta":
			if partial := partials[event.OutputIndex]; partial != nil {
				partial.arguments.WriteString(event.Delta)
				if hook != nil {
					// 上报本片的原始参数增量，并附上已解析的元数据。
					hook(ToolCallDelta{
						Index:     event.OutputIndex,
						ID:        partial.id,
						Name:      partial.name,
						Arguments: event.Delta,
					})
				}
			}
		case "response.function_call_arguments.done":
			// 参数完成事件携带最终、完整的参数字符串。忽略它会让"只发该事件、
			// 不发增量"的网关（或增量丢失的流）回退到 "{}"，导致工具以错误参数
			// 被调用。这里的参数是权威值，覆盖此前累积的增量；事件未携带参数时
			// 保留增量，避免用空串抹掉已收到的内容。
			partial := partials[event.OutputIndex]
			if partial == nil {
				// 该索引的调用已被 output_item.done 组装并停放。此时若
				// 新建一个部分项，最终组装阶段的 push 会因名称为空而跳过它，
				// 权威参数就会被静默丢弃，工具仍以旧参数被派发。事件乱序是
				// 本客户端已经容忍的形态（见 completed 的说明），因此直接
				// 更新已完成的调用。
				if call, ok := completed[event.OutputIndex]; ok {
					if event.Arguments != "" {
						call.Function.Arguments = integrity.toolArguments(event.Arguments)
					}
					if call.ID == "" && event.ItemID != "" {
						call.ID = event.ItemID
					}
					if call.Function.Name == "" {
						call.Function.Name = event.Name
					}
					completed[event.OutputIndex] = call
					continue
				}
				partial = &responsesPartial{}
				order = append(order, event.OutputIndex)
				partials[event.OutputIndex] = partial
			}
			partial.id = firstNonEmpty(partial.id, event.ItemID)
			partial.name = firstNonEmpty(partial.name, event.Name)
			if event.Arguments != "" {
				partial.arguments.Reset()
				partial.arguments.WriteString(event.Arguments)
			}
		case "response.output_item.done":
			if event.Item == nil || event.Item.Type != "function_call" {
				continue
			}
			partial := partials[event.OutputIndex]
			if partial == nil {
				partial = &responsesPartial{}
				order = append(order, event.OutputIndex)
				partials[event.OutputIndex] = partial
			}
			partial.id = firstNonEmpty(partial.id, event.Item.CallID, event.Item.ID)
			partial.name = firstNonEmpty(partial.name, event.Item.Name)
			// 与 response.function_call_arguments.done 一致：这里的 item
			// 是完整的输出项，其 arguments 是本次调用参数的权威值。网关
			// 可能只发送增量（且增量在传输中丢失或被截断），或者只在
			// output_item.added 中附带部分参数；此时必须用完整参数覆盖
			// 此前累积的片段，否则工具会以错误的 "{}" 参数被派发。
			// 事件未携带参数时保留增量，避免用空串抹掉已收到的内容。
			if event.Item.Arguments != "" {
				partial.arguments.Reset()
				partial.arguments.WriteString(event.Item.Arguments)
			}
			push(event.OutputIndex)
		case "response.completed", "response.incomplete":
			if event.Response != nil {
				// Responses 在 incomplete_details.reason 中报告截断
				// （如 "max_output_tokens"）；否则回退到原始状态。
				if event.Response.IncompleteDetails != nil && event.Response.IncompleteDetails.Reason != "" {
					finishReason = event.Response.IncompleteDetails.Reason
				} else if event.Response.Status != "" {
					finishReason = event.Response.Status
				}
			}
			if event.Response != nil && event.Response.Usage != nil {
				usage = &Usage{
					PromptTokens:     event.Response.Usage.InputTokens,
					CompletionTokens: event.Response.Usage.OutputTokens,
					CacheWriteTokens: event.Response.Usage.CacheWriteTokens,
				}
				if details := event.Response.Usage.InputTokensDetails; details != nil {
					usage.CachedTokens = details.CachedTokens
					// 细分字段优先：同一份用量里若两处都出现，
					// input_tokens_details 是更精确的来源。
					if details.CacheWriteTokens > 0 {
						usage.CacheWriteTokens = details.CacheWriteTokens
					}
				}
			}
			integrity.terminated = true
			break streamLoop
		case "response.failed", "error":
			var fromResponse string
			if event.Response != nil {
				fromResponse = errorMessage(event.Response.Error)
			}
			message := firstNonEmpty(
				fromResponse,
				errorMessage(event.Error),
				event.Message,
			)
			if message == "" {
				message = "unknown provider error"
			}
			return Message{}, streamError("provider error: %s", RedactText(message))
		}
	}

	for _, index := range order {
		push(index)
	}
	// 先收集提供方 id：只有知道回复中每个真实 id 后，
	// 回退 id 才安全，否则生成的 "call_N" 可能与某个重复。
	used := make(map[string]struct{}, len(order))
	for _, index := range order {
		if call, ok := completed[index]; ok && call.ID != "" {
			used[call.ID] = struct{}{}
		}
	}
	// 按宣布顺序而非完成顺序组装。done 处理器可能创建没有对应 added 的
	// 部分项（有些网关只发 done），这会第二次追加某个索引；
	// 每个调用只发出一次。
	calls := make([]ToolCall, 0, len(order))
	emitted := make(map[int]bool, len(order))
	for _, index := range order {
		if emitted[index] {
			continue
		}
		emitted[index] = true
		if call, ok := completed[index]; ok {
			if call.ID == "" {
				call.ID = uniqueToolCallID(used, "call_", index+1)
			}
			calls = append(calls, call)
		}
	}
	if err := integrity.eofError("response.completed"); err != nil {
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

func errorMessage(err *responsesError) string {
	if err == nil {
		return ""
	}
	return err.Message
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
