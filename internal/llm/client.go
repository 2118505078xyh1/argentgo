// Package llm 实现与 OpenAI 兼容的 chat completions 协议。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Message 是对话中的一条记录。
//
// Reasoning、Usage 和 CacheControl 是转录元数据：由协议客户端附加到
// 助手回复或稳定前缀请求上。它们
// 并非普通的上游字段（参见 Chat）。
type Message struct {
	Role               string     `json:"role"`
	Content            string     `json:"content,omitempty"`
	ToolCalls          []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID         string     `json:"tool_call_id,omitempty"`
	Reasoning          string     `json:"reasoning,omitempty"`
	ReasoningSignature string     `json:"reasoning_signature,omitempty"`
	Usage              *Usage     `json:"usage,omitempty"`
	// FinishReason 是提供方对本次回复的停止原因（例如 "stop"、
	// "tool_calls"、"length"）。它是线协议元数据，供 agent 循环用于
	// 拒绝被截断的工具调用批次，并非普通的转录内容。
	FinishReason string `json:"finish_reason,omitempty"`
	// CacheControl 为缓存网关标记稳定的系统消息。它
	// 从不会持久化到转录中。
	CacheControl *CacheControl `json:"-"`
	// ContentParts 承载多模态 content part 数组的原始 JSON，是绕过
	// Content string 的旁路字段：json:"-" 使其既不参与 Message 自身的
	// JSON 序列化，也不写入会话转录，仅由 wireMessages 消费。
	// nil 表示纯文本消息，走既有 text-only 路径。
	ContentParts json.RawMessage `json:"-"`
}

// Usage 报告提供方对一次回复计费的 token 数。
//
// PromptTokens 是完整的提示成本：新增 token 加上缓存 token。
// 缓存细分仅供参考，因此读取 PromptTokens 的预算与成本代码
// 应把每个 token 只计一次。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CachedTokens 是 PromptTokens 中由提示缓存提供的部分。
	CachedTokens int `json:"cached_tokens,omitempty"`
	// CacheWriteTokens 是 PromptTokens 中写入提示缓存的部分。
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`

	// 以下三个字段是一次流式输出的墙钟时间点（Unix 毫秒），只由三个协议的
	// readStream 在打点开关开启时填写。它们随 Usage 的值拷贝流到
	// agent.Event.Usage，最终由 app 桥接写入 TUI 的 FirstTokenAt /
	// StreamedAt / CompletedAt。
	//
	// json:"-" 是关键：这三个字段既不进入会话转录（Message.Usage 会被
	// 序列化到 JSONL），也不进入任何 SSE/RPC 帧。因此无论是否打点，
	// 既有字节都与接线前逐字节一致；它们只在进程内内存中传递。
	StreamRequestMS int64 `json:"-"`
	StreamFirstMS   int64 `json:"-"`
	StreamDoneMS    int64 `json:"-"`
}

// 以下原语为流式时间点打点提供 opt-in 开关。默认关闭时 readStream 既不调用
// time.Now，也不包装 onText，更不改写 Usage，因此所有既有请求的字节与内存
// 行为都与接入前完全一致；只有 TUI pipeline 这类需要速率显示的消费者才会
// 通过 EnableStreamTiming 开启。
//
// streamTimingEnabled 用引用计数控制开关（>0 表示开启），使多个 TUI pipeline
// 并存时各自的 disable 互不干扰。
var streamTimingEnabled atomic.Int64

// EnableStreamTiming 开启流式时间点打点，返回一个幂等的关闭函数。
// 每次调用都增加引用计数，返回的 disable 只减少一次，因此可以嵌套/并存。
func EnableStreamTiming() func() {
	streamTimingEnabled.Add(1)
	var once sync.Once
	return func() { once.Do(func() { streamTimingEnabled.Add(-1) }) }
}

// streamTimingOn 报告当前是否开启流式时间点打点。
func streamTimingOn() bool { return streamTimingEnabled.Load() > 0 }

// streamTiming 在单次 readStream 调用内收集三个时间点（Unix 毫秒）。
type streamTiming struct {
	requestMS int64
	firstMS   int64
	doneMS    int64
}

// wrapOnText 在第一次文本增量到达时打点，随后原样转发给下游回调。
// 推理增量不经过 onText，因此 firstMS 只反映首个输出文本 token。
func (t *streamTiming) wrapOnText(onText func(string)) func(string) {
	if onText == nil {
		return nil
	}
	return func(delta string) {
		if t.firstMS == 0 {
			t.firstMS = time.Now().UnixMilli()
		}
		onText(delta)
	}
}

// stamp 在流正常结束、组装最终 Usage 时写入时间点。usage 为 nil 时创建
// 一个仅承载时间点的空 Usage——token 字段仍为零，agent 的用量估算逻辑不变。
func (t *streamTiming) stamp(usage **Usage) {
	t.doneMS = time.Now().UnixMilli()
	if *usage == nil {
		*usage = &Usage{}
	}
	(*usage).StreamRequestMS = t.requestMS
	(*usage).StreamFirstMS = t.firstMS
	(*usage).StreamDoneMS = t.doneMS
}

// TotalTokens 返回一次回复的可计费 token 数，其中缓存的提示 token
// 仅通过 PromptTokens 计入。
func (u Usage) TotalTokens() int {
	return u.PromptTokens + u.CompletionTokens
}

// FreshPromptTokens 返回未由缓存提供的提示 token。
func (u Usage) FreshPromptTokens() int {
	if u.CachedTokens >= u.PromptTokens {
		return 0
	}
	return u.PromptTokens - u.CachedTokens
}

const (
	// cacheWarmWindowStart 是缓存条目生命周期中，在此比例之后
	// 保活请求才有资格发出。pi 在生命周期 90% 处设置单个定时器
	// （留十秒余量）；这里取 80% 作为下限，给调用方
	// 在该区间内调度的余地，避免与过期时间竞争。
	cacheWarmWindowStart = 0.8

	// maxIdleWarmAge 是 pi 空闲期的安全上限：空闲达到此时长后，条目
	// 即使在 TTL 更长的情况下也不会再被保活。
	maxIdleWarmAge = 30 * time.Minute
)

// ShouldWarmCache 报告是否值得为某个提示缓存条目发送保活（"rent"）请求。
// 它是纯决策辅助函数：既不发送请求，
// 也不触碰任何客户端状态，因此调用方可以在轮次边界的定时器上启用它，
// 而无需将线协议与 agent 循环耦合。
//
// 条目在 lastRequest 时最后被写入或读取，并在 ttl 这个绝对截止时间过期
// （通常为 lastRequest 加上提供方的缓存生存期）。
// 只有在该生存期的最后阶段、截止时间之前，且空闲间隔未超过
// maxIdleWarmAge 时，保活才有资格。
// 经济性门槛要求预期节省的未命中成本
// (missCost * continuationProb) 超过保活自身的成本。
//
// idleSince 为零会禁用空闲时长检查，适用于不跟踪空闲时间的调用方。
// NaN 成本无法通过经济性门槛，因为与 NaN 的任何比较
// 结果都是 false。
func ShouldWarmCache(now, lastRequest, ttl, idleSince time.Time, warmCost, missCost, continuationProb float64) bool {
	// 已过期或状态不一致的条目无法保活。下面的窗口检查
	// 也会拒绝早于 lastRequest 的 now（时钟偏差或新条目）。
	if now.After(ttl) || !ttl.After(lastRequest) || now.Before(lastRequest) {
		return false
	}
	// 空闲安全上限：两次真实请求间隔超过 30 分钟后 pi 停止保活。
	// 等于或晚于 now 的 idleSince 会得到非正的空档时长，不构成上限。
	if !idleSince.IsZero() && now.Sub(idleSince) > maxIdleWarmAge {
		return false
	}
	lifetime := ttl.Sub(lastRequest)
	elapsed := now.Sub(lastRequest)
	if float64(elapsed) < cacheWarmWindowStart*float64(lifetime) {
		return false
	}
	return missCost*continuationProb > warmCost
}

// CacheWarmer 是面向长生命周期引擎的、可复用的、无网络访问的保活调度器。
// 它记录真实请求何时刷新了提示缓存，以及
// agent 何时进入空闲，然后回答 ShouldWarm，让调用方决定是否
// 发送一个 1-token 的重放请求。它自身从不发送请求或启动定时器；
// 重放完全交由调用方处理。
//
// 所有可变状态都由互斥锁保护，因此后台定时器 goroutine 可以与
// 调用 RecordRequest 和 RecordIdle 的 agent 循环并发调用 ShouldWarm。
// 零值表示禁用：在调用方通过设置 Enabled 选择启用之前，
// ShouldWarm 始终返回 false，从而保持既有请求行为不变。
type CacheWarmer struct {
	mu sync.Mutex

	// Enabled 用于启用保活调度器。默认禁用（零值）。
	Enabled bool

	// TTL 是从上一次真实请求起算的提供方缓存生存期。
	// 非正值将回退到 defaultWarmTTL。
	TTL time.Duration

	// WarmCost、MissCost 和 ContinuationProb 驱动 ShouldWarmCache 的经济性
	// 门槛。三者都未设置（为零）时使用保守的默认值。
	WarmCost         float64
	MissCost         float64
	ContinuationProb float64

	lastRequest time.Time
	idleSince   time.Time
}

const (
	// defaultWarmTTL 是缓存网关接受的最短保留档位，
	// 在 CacheWarmer 未设置 TTL 时使用。
	defaultWarmTTL = 5 * time.Minute
	// 与重建整个提示缓存相比，1-token 重放代价很低：
	// 这些默认值在适中的续接概率下能通过经济性门槛，
	// 但当调用方设置非正的未命中成本时仍无法通过。
	defaultWarmCost         = 1.0
	defaultMissCost         = 10.0
	defaultContinuationProb = 0.5
)

// RecordRequest 记录一次真实请求在 now 刷新了提示缓存。
// 它会重启两个时钟：缓存生存期和保活窗口都从本次请求起算，
// 空闲阶段结束，直到轮次边界上下一次 RecordIdle 为止，
// 因此过期的空闲时间不会抑制新的窗口。
func (cw *CacheWarmer) RecordRequest(now time.Time) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.lastRequest = now
	cw.idleSince = time.Time{}
}

// RecordIdle 记录 agent 在 now 进入空闲，通常发生在轮次边界。
// 它会重置空闲时长上限，避免一个长期运行但最近仍活跃的 agent
// 被误判为空闲已超过 maxIdleWarmAge。
func (cw *CacheWarmer) RecordIdle(now time.Time) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.idleSince = now
}

// ShouldWarm 报告在 now 是否该进行保活重放。可以安全地与
// RecordRequest 和 RecordIdle 并发调用。被禁用的调度器、
// 从未见过请求的调度器，或经济性门槛未通过的调度器，
// 都返回 false。
func (cw *CacheWarmer) ShouldWarm(now time.Time) bool {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if !cw.Enabled || cw.lastRequest.IsZero() {
		return false
	}
	ttl := cw.TTL
	if ttl <= 0 {
		ttl = defaultWarmTTL
	}
	warmCost, missCost, prob := cw.WarmCost, cw.MissCost, cw.ContinuationProb
	if warmCost == 0 && missCost == 0 && prob == 0 {
		warmCost, missCost, prob = defaultWarmCost, defaultMissCost, defaultContinuationProb
	}
	return ShouldWarmCache(now, cw.lastRequest, cw.lastRequest.Add(ttl), cw.idleSince, warmCost, missCost, prob)
}

// Chatter 由本包中每个流式协议客户端实现。
type Chatter interface {
	Chat(ctx context.Context, model string, messages []Message, tools []ToolSpec, onText func(string)) (Message, error)
}

// ToolCall 是模型发出的运行工具的请求。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 携带工具名称和原始 JSON 参数。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSpec 是向模型声明的 JSON schema。
type ToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// Client 是面向 OpenAI 兼容 chat completions 的最小化流式客户端。
// 除非设置了 Options.ForcePromptCache，否则不使用缓存。
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	Retry   RetryPolicy
	Options Options
	// IdleTimeout 限制流事件之间的静默时长。一个已打开的连接可能
	// 停止传送数据却从不报错；设置正的 IdleTimeout 后，读取会作为可重试的
	// 流错误失败，而不是一直挂起直到调用方的 context 过期。
	// 零值（默认）保持同步读取，不增加定时器或 goroutine，
	// 因此现有调用方不受影响。
	IdleTimeout time.Duration
}

// New 返回一个面向 OpenAI 兼容端点的客户端。
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{},
	}
}

// PromptCacheCapability 报告此客户端是否发出缓存标记。
func (c *Client) PromptCacheCapability() CacheCapability {
	return CacheCapability{
		Supported: c.Options.cacheEnabled(ProtocolChatCompletions, c.BaseURL),
		Protocol:  ProtocolChatCompletions,
		Local:     isLocalEndpoint(c.BaseURL),
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

type chatRequest struct {
	Model                string         `json:"model"`
	Messages             []chatMessage  `json:"messages"`
	Tools                []ToolSpec     `json:"tools,omitempty"`
	StopTokenIDs         []int64        `json:"stop_token_ids,omitempty"`
	Stream               bool           `json:"stream"`
	StreamOptions        *streamOptions `json:"stream_options,omitempty"`
	PromptCacheRetention string         `json:"prompt_cache_retention,omitempty"`
	// ReasoningEffort 是 OpenAI 风格推理级别，映射到 reasoning_effort。
	// 零值（空串）由 omitempty 省略，使默认请求字节与接线前完全一致。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Verbosity 是 OpenAI 风格输出详细度，映射到 verbosity。
	// 零值（空串）由 omitempty 省略，使默认请求字节与接线前完全一致。
	Verbosity string `json:"verbosity,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage 是一条聊天消息的线协议形态。只有在显式启用缓存时，
// 才会把 CacheControl 加到第一条稳定的系统消息上。
type chatMessage struct {
	Role         string        `json:"role"`
	Content      string        `json:"content,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
	// ReasoningContent 是在启用 Options.PreserveReasoning 时，
	// 在助手轮次上重放的推理内容。默认保持为空，
	// 以使所有现有调用方发出的线协议字节保持不变。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// Parts 是已编码好的 content part 数组原始 JSON。非 nil 时作为
	// content 原样输出；json:"-" 让默认编码路径（别名 plain）不会发出它。
	Parts json.RawMessage `json:"-"`
}

// MarshalJSON 为带图消息把原始 part 数组直接放到 content 上，避免把
// 已编码的 JSON 再当成字符串二次编码（那会让提供方收到字面文本）。
// Parts 为 nil 时委托给去掉方法的别名类型，text-only 字节保持逐字节不变。
func (m chatMessage) MarshalJSON() ([]byte, error) {
	type plain chatMessage
	if m.Parts == nil {
		return json.Marshal(plain(m))
	}
	// 独立的输出结构：Content 用 json.RawMessage 承载原始数组，
	// 其余字段沿用原有的标签与顺序。
	return json.Marshal(struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content,omitempty"`
		ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
		CacheControl     *CacheControl   `json:"cache_control,omitempty"`
		ReasoningContent string          `json:"reasoning_content,omitempty"`
	}{
		Role:             m.Role,
		Content:          m.Parts,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		CacheControl:     m.CacheControl,
		ReasoningContent: m.ReasoningContent,
	})
}

// wireMessages 复制消息时不带仅用于转录的字段。在
// preserveReasoning 关闭（默认）时，它保持上游字节与 Reasoning/Usage 出现之前
// 完全一致；仅当显式开启该标志时，才会给携带推理的助手轮次
// 添加 reasoning_content 字段。该 bool 参数是可变的，
// 以便历史的三参数调用形式仍能编译。
func wireMessages(messages []Message, cacheEnabled bool, control *CacheControl, preserveReasoning ...bool) []chatMessage {
	keepReasoning := len(preserveReasoning) > 0 && preserveReasoning[0]
	out := make([]chatMessage, len(messages))
	marked := false
	for i, m := range messages {
		out[i] = chatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			Parts:      m.ContentParts,
		}
		if keepReasoning && m.Role == "assistant" && m.Reasoning != "" {
			out[i].ReasoningContent = m.Reasoning
		}
		if cacheEnabled && !marked && m.Role == "system" && strings.TrimSpace(m.Content) != "" {
			out[i].CacheControl = control
			marked = true
		}
	}
	return out
}

var _ Chatter = (*Client)(nil)
var _ PromptCacheCapable = (*Client)(nil)

type chunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// ReasoningContent 是 OpenAI 兼容的增量推理字段
			// （DeepSeek、Qwen 等）。Reasoning 是部分网关使用的
			// 备用键。每个 delta 只写入其中一个；取第一个非空值，
			// 这样即使提供方同时发送两者，文本也不会重复。
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat 流式获取一次补全并返回组装好的助手消息。
// onText 非空时，会在内容增量到达时接收它们。
func (c *Client) Chat(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
	onText func(string),
) (Message, error) {
	return c.chat(ctx, model, messages, tools, onText, nil, c.reasoningEffort())
}

// reasoningEffort 返回 OpenAI chat completions 的 reasoning_effort 值。
// 仅当显式设置 Options.Thinking 时才取其 Effort；默认为空串，
// 使请求线协议字节与接线前完全一致。FIM 专用路径不调用它。
func (c *Client) reasoningEffort() string {
	if c.Options.Thinking == nil {
		return ""
	}
	return c.Options.Thinking.Effort
}

// buildChatRequest 按默认（流式）语义构造 chat completions 请求体。
// 它集中处理缓存控制、tools、stop_token_ids、推理级别与输出详细度的线协议字段，
// 供 Chat/FillMiddle 的流式路径与 ChatOnce 的非流式路径复用。
// 返回值的字段及其顺序与提取前 chat() 内联构造的代码完全一致；
// ChatOnce 在调用后显式关闭 Stream 并移除 StreamOptions。
func (c *Client) buildChatRequest(
	model string,
	messages []Message,
	tools []ToolSpec,
	stopIDs []int64,
	reasoningEffort string,
) chatRequest {
	cacheEnabled := c.Options.cacheEnabled(ProtocolChatCompletions, c.BaseURL)
	request := chatRequest{
		Model:           model,
		Messages:        wireMessages(messages, cacheEnabled, c.Options.cacheControl(), c.Options.PreserveReasoning),
		Tools:           tools,
		StopTokenIDs:    stopIDs,
		Stream:          true,
		ReasoningEffort: reasoningEffort,
		Verbosity:       c.Options.Verbosity,
	}
	if cacheEnabled || c.Options.IncludeUsage {
		request.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if cacheEnabled {
		request.PromptCacheRetention = c.Options.CacheRetention
	}
	return request
}

// chat 是共享的请求/重试/流式处理管线。stopIDs 非空时，
// 会作为可选的 stop_token_ids 字段转发；Chat 传入 nil，
// 因此其线协议格式不变。FillMiddle 用它来附加 FIM 停止 token。
// reasoningEffort 为本次请求要转发的 reasoning_effort：Chat 从
// Options.Thinking 派生，FillMiddle 始终传空串，使 FIM 永不携带推理参数。
func (c *Client) chat(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
	onText func(string),
	stopIDs []int64,
	reasoningEffort string,
) (Message, error) {
	start := time.Now() // 请求级 trace 计时；未安装 sink 时 recordTrace 立即返回
	request := c.buildChatRequest(model, messages, tools, stopIDs, reasoningEffort)
	payload, err := json.Marshal(request)
	if err != nil {
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
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
				Protocol:   ProtocolChatCompletions,
				Model:      model,
				Attempt:    attempt.Attempt,
				StatusCode: attempt.StatusCode,
				ErrorClass: attempt.Cause,
			})
		}
	}
	resp, err := policy.Do(ctx, func(ctx context.Context) (*http.Response, error) {
		// 每次尝试都重新创建请求（及 body reader）：流式 body
		// 无法重放，而 RetryPolicy.Do 在失败时会再次调用 send。
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		ApplyExtraHeaders(req) // 可选覆盖/追加请求头（如 Azure 的 api-key）；默认无注入时 no-op
		return c.httpClient().Do(req)
	})
	if err != nil {
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolChatCompletions, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		err := providerError(resp)
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolChatCompletions, model, err))
		return Message{}, err
	}

	reply, err := c.readStream(ctx, model, resp.Body, onText)
	if err != nil {
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolChatCompletions, model, err))
		return Message{}, err
	}
	recordTrace(ProtocolChatCompletions, model, reply.Usage, start, nil, "ok")
	emitAttempt(c.Options.AttemptHook, AttemptRecord{Kind: AttemptSuccess, Protocol: ProtocolChatCompletions, Model: model})
	return reply, nil
}

// chatOnceResponse 是非流式 chat completions 的响应载荷。
// 与流式 chunk 不同，助手内容位于 choices[].message 中，且工具参数
// 是一次性给出的完整 JSON 字符串，而非跨增量拼接的片段。
type chatOnceResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatOnce 以非流式方式获取一次补全并返回助手消息。
// 它服务于流中断回退：服务端返回单个 JSON 而不是 SSE，因此不消费流，
// 直接把响应用作一次完整的 Message。请求/重试/鉴权/trace/attempt 路径
// 沿用 chat()，仅 Accept 改为 application/json，并去掉与 Stream=false
// 互斥的 stream_options（二者同时出现会被引擎拒绝）。
func (c *Client) ChatOnce(ctx context.Context, model string, messages []Message, tools []ToolSpec) (Message, error) {
	start := time.Now()
	request := c.buildChatRequest(model, messages, tools, nil, c.reasoningEffort())
	// stream_options 只在流式请求中用于请求 usage；非流式下必须清除。
	request.Stream = false
	request.StreamOptions = nil
	payload, err := json.Marshal(request)
	if err != nil {
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
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
				Protocol:   ProtocolChatCompletions,
				Model:      model,
				Attempt:    attempt.Attempt,
				StatusCode: attempt.StatusCode,
				ErrorClass: attempt.Cause,
			})
		}
	}
	resp, err := policy.Do(ctx, func(ctx context.Context) (*http.Response, error) {
		// 每次尝试都重新创建请求（及 body reader），与流式路径保持一致。
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		ApplyExtraHeaders(req)
		return c.httpClient().Do(req)
	})
	if err != nil {
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolChatCompletions, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		err := providerError(resp)
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolChatCompletions, model, err))
		return Message{}, err
	}

	var response chatOnceResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		err = fmt.Errorf("decode response: %w", err)
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolChatCompletions, model, err))
		return Message{}, err
	}
	if response.Error != nil && response.Error.Message != "" {
		err := fmt.Errorf("provider error: %s", RedactText(response.Error.Message))
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolChatCompletions, model, err))
		return Message{}, err
	}
	fail := func(err error) (Message, error) {
		recordTrace(ProtocolChatCompletions, model, nil, start, err, "")
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolChatCompletions, model, err))
		return Message{}, err
	}
	if len(response.Choices) == 0 {
		return fail(fmt.Errorf("provider returned no choices"))
	}

	choice := response.Choices[0]
	// 推理字段优先使用 OpenAI 兼容的 reasoning_content，
	// 缺失时回退到部分网关使用的备用拼写 reasoning。
	reasoning := choice.Message.ReasoningContent
	if reasoning == "" {
		reasoning = choice.Message.Reasoning
	}
	if choice.Message.Content == "" && reasoning == "" && len(choice.Message.ToolCalls) == 0 {
		return fail(fmt.Errorf("provider returned an empty message"))
	}

	var usage *Usage
	if response.Usage != nil {
		usage = &Usage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
		}
		if details := response.Usage.PromptTokensDetails; details != nil {
			usage.CachedTokens = details.CachedTokens
		}
	}

	// 生成的调用 id 不得与提供方提供的 id 冲突；重复的调用 id
	// 会让 agent 拒绝整个工具批次。
	used := make(map[string]struct{}, len(choice.Message.ToolCalls))
	for _, tc := range choice.Message.ToolCalls {
		if tc.ID != "" {
			used[tc.ID] = struct{}{}
		}
	}
	// integrity 标记为已终止：非流式响应本身就是完整答复，
	// 参数补全只用于把空/非法参数安全回退为 "{}"，不触发截断判定。
	integrity := &streamIntegrity{terminated: true}
	calls := make([]ToolCall, 0, len(choice.Message.ToolCalls))
	for i, tc := range choice.Message.ToolCalls {
		callType := tc.Type
		if callType == "" {
			callType = "function"
		}
		call := ToolCall{
			ID:   tc.ID,
			Type: callType,
			Function: ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: integrity.toolArguments(tc.Function.Arguments),
			},
		}
		if call.ID == "" {
			call.ID = uniqueToolCallID(used, "call_", i+1)
		}
		calls = append(calls, call)
	}

	reply := Message{
		Role:         "assistant",
		Content:      choice.Message.Content,
		ToolCalls:    calls,
		Reasoning:    reasoning,
		Usage:        usage,
		FinishReason: choice.FinishReason,
	}
	recordTrace(ProtocolChatCompletions, model, reply.Usage, start, nil, "ok")
	emitAttempt(c.Options.AttemptHook, AttemptRecord{Kind: AttemptSuccess, Protocol: ProtocolChatCompletions, Model: model})
	return reply, nil
}

func (c *Client) readStream(ctx context.Context, model string, body io.Reader, onText func(string)) (Message, error) {
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
	var text strings.Builder
	// 推理增量按完整字符串原样追加，因此跨分块拆开的 CJK 字符
	// 会被精确保留：不进行任何字节/字符切片。
	var reasoning strings.Builder
	// 工具调用同时以流索引和 id 作为键。网关可能在增量之间
	// 重新编号调用或交换其 id；两个映射都指向同一个
	// 块，因此每个片段仍会落到同一处。blocks 按创建顺序
	// 保存各个不同的调用，用于生成最终结果。
	blocks := make([]*ToolCall, 0, 2)
	byIndex := make(map[int]*ToolCall)
	byID := make(map[string]*ToolCall)
	var usage *Usage
	var finishReason string
	var integrity streamIntegrity

	// 复用共享的 SSE 读取器：单个事件的 JSON 载荷可能横跨多个 data: 行，
	// 这是 SSE 规范允许且网关会使用的做法。
	// idleReader 统一承担连接静默检测；三个协议共享同一实现，
	// IdleTimeout == 0 时它直接同步读取，不增加定时器或 goroutine。
	idleReader := newSSEIdleReader(newSSEReader(body), c.IdleTimeout)
	defer idleReader.stop()
	var pending []string

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
				break streamLoop
			}
			// 一个 SSE 事件可能携带多个载荷：跨 data: 行拆开的 JSON 对象，
			// 或没有空行分隔的独立事件。
			// 通过下面相同的路径逐个解码。
			pending = splitSSEData(next)
			data, pending = pending[0], pending[1:]
		}
		// 空的 data: 行是合法的 SSE 保活信号，不是 JSON 事件。
		// 跳过它可以避免代理的心跳导致回复失败。
		if isEmptySSEData(data) {
			continue
		}
		if data == "[DONE]" {
			integrity.terminated = true
			break streamLoop
		}

		var event chunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Message{}, streamError("decode stream event: %v", RedactError(err))
		}
		if event.Error != nil {
			return Message{}, streamError("provider error: %s", RedactText(event.Error.Message))
		}
		if event.Usage != nil {
			next := &Usage{
				PromptTokens:     event.Usage.PromptTokens,
				CompletionTokens: event.Usage.CompletionTokens,
			}
			if details := event.Usage.PromptTokensDetails; details != nil {
				next.CachedTokens = details.CachedTokens
			}
			// Usage 通常只在 [DONE] 前的最后一个分块中上报一次。
			// 网关也会发出部分快照：全零的
			// 终止快照、仅包含提示计数的早期快照，或只携带缓存 token 细分的
			// 最终分块。应逐字段合并，
			// 而不是替换整个快照：零值字段表示
			// "本分块未上报"，绝不是"重置"。如果
			// 带数据的分块替换了先前的总计，那么仅含缓存的尾部
			// 会导致 CachedTokens > PromptTokens 且 TotalTokens == 0，
			// 在无声中破坏所有下游的预算计算。
			if usage == nil {
				usage = &Usage{}
			}
			if next.PromptTokens > 0 {
				usage.PromptTokens = next.PromptTokens
			}
			if next.CompletionTokens > 0 {
				usage.CompletionTokens = next.CompletionTokens
			}
			if next.CachedTokens > 0 {
				usage.CachedTokens = next.CachedTokens
			}
		}
		for _, choice := range event.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			if delta := choice.Delta.Content; delta != "" {
				text.WriteString(delta)
				if onText != nil {
					onText(delta)
				}
			}
			// 捕获增量推理但不转发给 onText：
			// 推理是转录元数据，而非回答文本。优先使用
			// OpenAI 兼容的键；若不存在则回退到备用拼写。
			if delta := choice.Delta.ReasoningContent; delta != "" {
				reasoning.WriteString(delta)
			} else if delta := choice.Delta.Reasoning; delta != "" {
				reasoning.WriteString(delta)
			}
			for _, tc := range choice.Delta.ToolCalls {
				// 优先按 id 解析：提供方的 id 是调用的稳定标识，
				// 而 index 只是一个流位置，网关可能在流中途重新编号
				// （或在续接增量中省略）。已知 id 即使出现在旧索引下，
				// 也必须找到它自己的调用；若先信任索引，
				// 片段会被追加到曾占据该槽位的调用上，
				// 在无声中交换两个调用的参数。
				var call *ToolCall
				if tc.ID != "" {
					call = byID[tc.ID]
				}
				if call == nil {
					call = byIndex[tc.Index]
				}
				if call == nil {
					call = &ToolCall{Type: "function"}
					blocks = append(blocks, call)
				}
				// 把该块出现过的每个键都注册为别名。当某个已知 id
				// 以不同索引重新出现时，把该索引重新绑定到该 id 对应的调用，
				// 使新位置上后续没有 id 的增量也能
				// 落到正确的块中。
				if tc.ID != "" || byIndex[tc.Index] == nil {
					byIndex[tc.Index] = call
				}
				if tc.ID != "" {
					if byID[tc.ID] == nil {
						byID[tc.ID] = call
					}
					// 保留第一个 id：流中途的重命名是别名，不是
					// 第二次调用，而且 agent 以该 id 为结果建立索引。
					if call.ID == "" {
						call.ID = tc.ID
					}
				}
				call.Function.Name += tc.Function.Name
				call.Function.Arguments += tc.Function.Arguments
				if hook != nil {
					// 上报本片的原始增量（不混入累积值），调用方按 Index 自行归并。
					hook(ToolCallDelta{
						Index:     tc.Index,
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					})
				}
			}
		}
	}
	// 生成的 id 不得与提供方提供的 id 冲突：
	// 重复的调用 id 会让 agent 拒绝整个工具批次。
	used := make(map[string]struct{}, len(blocks))
	for _, block := range blocks {
		if block.ID != "" {
			used[block.ID] = struct{}{}
		}
	}
	calls := make([]ToolCall, 0, len(blocks))
	for i, block := range blocks {
		call := *block
		if call.ID == "" {
			call.ID = uniqueToolCallID(used, "call_", i+1)
		}
		// 被截断或格式错误的参数片段不能安全地交给
		// 工具分发器；应像其他协议客户端一样回退到空对象。
		// 如果流从未到达 [DONE]，则这种
		// 回退意味着回复在工具调用中途被截断：此时应直接失败。
		call.Function.Arguments = integrity.toolArguments(call.Function.Arguments)
		calls = append(calls, call)
	}
	if err := integrity.eofError("[DONE]"); err != nil {
		return Message{}, err
	}

	if timingOn {
		timing.stamp(&usage)
	}
	return Message{Role: "assistant", Content: text.String(), ToolCalls: calls, Reasoning: reasoning.String(), Usage: usage, FinishReason: finishReason}, nil
}
