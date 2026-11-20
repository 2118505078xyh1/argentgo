// Package llm speaks the OpenAI-compatible chat completions protocol.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is one entry in the conversation.
//
// Reasoning, Usage and CacheControl are transcript metadata: they are attached
// to assistant replies or stable-prefix requests by the protocol clients. They
// are not ordinary upstream fields (see Chat).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	// FinishReason is the provider's stop reason for this reply (e.g. "stop",
	// "tool_calls", "length"). It is wire metadata used by the agent loop to
	// refuse a truncated tool batch, not ordinary transcript content.
	FinishReason string `json:"finish_reason,omitempty"`
	// CacheControl marks a stable system message for a caching gateway. It is
	// never persisted in a transcript.
	CacheControl *CacheControl `json:"-"`
}

// Usage reports the tokens a provider billed for one reply.
//
// PromptTokens is the complete prompt cost: fresh plus cached tokens. The
// cached breakdown is informational, so budget and cost code that reads
// PromptTokens counts each token once.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CachedTokens is the portion of PromptTokens served from prompt cache.
	CachedTokens int `json:"cached_tokens,omitempty"`
	// CacheWriteTokens is the portion of PromptTokens written to prompt cache.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// TotalTokens returns billable tokens for one reply, counting cached prompt
// tokens only through PromptTokens.
func (u Usage) TotalTokens() int {
	return u.PromptTokens + u.CompletionTokens
}

// FreshPromptTokens returns prompt tokens that were not served from cache.
func (u Usage) FreshPromptTokens() int {
	if u.CachedTokens >= u.PromptTokens {
		return 0
	}
	return u.PromptTokens - u.CachedTokens
}

// Chatter is implemented by every streaming protocol client in this package.
type Chatter interface {
	Chat(ctx context.Context, model string, messages []Message, tools []ToolSpec, onText func(string)) (Message, error)
}

// ToolCall is a model request to run a tool.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction carries the name and raw JSON arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSpec is the JSON schema advertised to the model.
type ToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// Client is a minimal streaming client for OpenAI-compatible chat
// completions. It is cache-free unless Options.ForcePromptCache is set.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	Retry   RetryPolicy
	Options Options
}

// New returns a client for an OpenAI-compatible endpoint.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{},
	}
}

// PromptCacheCapability reports whether this client emits cache markers.
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
	Stream               bool           `json:"stream"`
	StreamOptions        *streamOptions `json:"stream_options,omitempty"`
	PromptCacheRetention string         `json:"prompt_cache_retention,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage is the wire shape of one chat message. CacheControl is added
// only to the first stable system message when caching is explicitly enabled.
type chatMessage struct {
	Role         string        `json:"role"`
	Content      string        `json:"content,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// wireMessages copies messages without transcript-only fields, keeping the
// bytes sent upstream exactly as before Reasoning/Usage existed.
func wireMessages(messages []Message, cacheEnabled bool, control *CacheControl) []chatMessage {
	out := make([]chatMessage, len(messages))
	marked := false
	for i, m := range messages {
		out[i] = chatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
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
			Content   string `json:"content"`
			ToolCalls []struct {
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

// Chat streams one completion and returns the assembled assistant message.
// onText, when non-nil, receives content deltas as they arrive.
func (c *Client) Chat(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
	onText func(string),
) (Message, error) {
	cacheEnabled := c.Options.cacheEnabled(ProtocolChatCompletions, c.BaseURL)
	request := chatRequest{
		Model:    model,
		Messages: wireMessages(messages, cacheEnabled, c.Options.cacheControl()),
		Tools:    tools,
		Stream:   true,
	}
	if cacheEnabled || c.Options.IncludeUsage {
		request.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if cacheEnabled {
		request.PromptCacheRetention = c.Options.CacheRetention
	}
	payload, err := json.Marshal(request)
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
				Protocol:   ProtocolChatCompletions,
				Model:      model,
				Attempt:    attempt.Attempt,
				StatusCode: attempt.StatusCode,
				ErrorClass: attempt.Cause,
			})
		}
	}
	resp, err := policy.Do(ctx, func(ctx context.Context) (*http.Response, error) {
		// A fresh request (and body reader) per attempt: streaming bodies
		// cannot be replayed, and RetryPolicy.Do calls send again on failure.
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		return c.httpClient().Do(req)
	})
	if err != nil {
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolChatCompletions, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		err := providerError(resp)
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolChatCompletions, model, err))
		return Message{}, err
	}

	reply, err := c.readStream(ctx, model, resp.Body, onText)
	if err != nil {
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolChatCompletions, model, err))
		return Message{}, err
	}
	emitAttempt(c.Options.AttemptHook, AttemptRecord{Kind: AttemptSuccess, Protocol: ProtocolChatCompletions, Model: model})
	return reply, nil
}

func (c *Client) readStream(ctx context.Context, model string, body io.Reader, onText func(string)) (Message, error) {
	var text strings.Builder
	order := make([]int, 0, 2)
	partial := make(map[int]*ToolCall)
	var usage *Usage
	var finishReason string

	// Reuse the shared SSE reader: a single event may carry its JSON payload
	// across several data: lines, which the SSE spec allows and gateways use.
	reader := newSSEReader(body)
	for {
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		data, ok, err := reader.next()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Message{}, ctxErr
			}
			return Message{}, streamError("read stream: %v", RedactError(err))
		}
		if !ok {
			break
		}
		if data == "[DONE]" {
			break
		}

		var event chunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Message{}, streamError("decode stream event: %v", RedactError(err))
		}
		if event.Error != nil {
			return Message{}, streamError("provider error: %s", RedactText(event.Error.Message))
		}
		if event.Usage != nil {
			usage = &Usage{
				PromptTokens:     event.Usage.PromptTokens,
				CompletionTokens: event.Usage.CompletionTokens,
			}
			if details := event.Usage.PromptTokensDetails; details != nil {
				usage.CachedTokens = details.CachedTokens
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
			for _, tc := range choice.Delta.ToolCalls {
				call, ok := partial[tc.Index]
				if !ok {
					call = &ToolCall{Type: "function"}
					partial[tc.Index] = call
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					call.ID = tc.ID
				}
				call.Function.Name += tc.Function.Name
				call.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Message{}, ctxErr
		}
		return Message{}, streamError("read stream: %v", RedactError(err))
	}

	calls := make([]ToolCall, 0, len(order))
	for i, index := range order {
		call := *partial[index]
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d", i+1)
		}
		if strings.TrimSpace(call.Function.Arguments) == "" {
			call.Function.Arguments = "{}"
		}
		calls = append(calls, call)
	}

	return Message{Role: "assistant", Content: text.String(), ToolCalls: calls, Usage: usage, FinishReason: finishReason}, nil
}
