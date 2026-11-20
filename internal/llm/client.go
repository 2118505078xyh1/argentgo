// Package llm speaks the OpenAI-compatible chat completions protocol.
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
	"time"
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

const (
	// cacheWarmWindowStart is the fraction of a cache entry's lifetime after
	// which a keepalive becomes eligible. pi arms a single timer at 90% of the
	// lifetime (with a ten-second margin); the 80% floor here leaves a caller
	// room to schedule inside that band without racing the expiry.
	cacheWarmWindowStart = 0.8

	// maxIdleWarmAge is the pi idle-phase safety cap: an entry is never warmed
	// once the agent has been idle this long, even when its TTL is longer.
	maxIdleWarmAge = 30 * time.Minute
)

// ShouldWarmCache reports whether a keepalive ("rent") request is worth sending
// for a prompt cache entry. It is a pure decision helper: it neither sends a
// request nor touches any client state, so a caller can arm it from a
// turn-boundary timer without coupling wire protocols to the agent loop.
//
// The entry was last written or read at lastRequest and expires at ttl, an
// absolute deadline (normally lastRequest plus the provider's cache lifetime).
// A warm is eligible only in the final stretch of that lifetime, before the
// deadline, and while the idle interval has not exceeded maxIdleWarmAge. The
// economics gate requires the expected avoided miss cost
// (missCost * continuationProb) to exceed the keepalive's own cost.
//
// A zero idleSince disables the idle-age check, for callers that do not track
// idle time. NaN costs fail the economics gate, since every comparison with
// NaN is false.
func ShouldWarmCache(now, lastRequest, ttl, idleSince time.Time, warmCost, missCost, continuationProb float64) bool {
	// An expired or inconsistent entry cannot be kept alive. The window check
	// below also rejects a now before lastRequest (clock skew or a fresh entry).
	if now.After(ttl) || !ttl.After(lastRequest) || now.Before(lastRequest) {
		return false
	}
	// Idle safety cap: pi stops after 30 minutes between real requests. An
	// idleSince at or after now yields a non-positive age and is not a cap.
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

// CacheWarmer is a reusable, network-free keepalive scheduler for long-lived
// engines. It records when a real request refreshed the prompt cache and when
// the agent went idle, then answers ShouldWarm so a caller can decide whether
// to send a 1-token replay. It never sends a request or starts a timer itself;
// the replay is left entirely to the caller.
//
// All mutable state is guarded by a mutex, so a background timer goroutine may
// call ShouldWarm concurrently with the agent loop calling RecordRequest and
// RecordIdle. The zero value is disabled: ShouldWarm returns false until a
// caller opts in by setting Enabled, leaving existing request behaviour intact.
type CacheWarmer struct {
	mu sync.Mutex

	// Enabled opts the warmer in. Disabled by default (zero value).
	Enabled bool

	// TTL is the provider cache lifetime measured from the last real request.
	// A non-positive value falls back to defaultWarmTTL.
	TTL time.Duration

	// WarmCost, MissCost and ContinuationProb drive ShouldWarmCache's economics
	// gate. When all three are unset (zero) conservative defaults are used.
	WarmCost         float64
	MissCost         float64
	ContinuationProb float64

	lastRequest time.Time
	idleSince   time.Time
}

const (
	// defaultWarmTTL is the shortest retention tier the caching gateways
	// accept, used when a CacheWarmer leaves TTL unset.
	defaultWarmTTL = 5 * time.Minute
	// A 1-token replay is cheap relative to rebuilding a full prompt cache:
	// these defaults clear the economics gate at a modest continuation chance,
	// but still fail it when a caller sets a non-positive miss cost.
	defaultWarmCost         = 1.0
	defaultMissCost         = 10.0
	defaultContinuationProb = 0.5
)

// RecordRequest records that a real request refreshed the prompt cache at now.
// It restarts both clocks: the cache lifetime and warm window are measured from
// this request, and the idle phase ends until the next RecordIdle at a turn
// boundary, so a stale idle time can never suppress a fresh window.
func (cw *CacheWarmer) RecordRequest(now time.Time) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.lastRequest = now
	cw.idleSince = time.Time{}
}

// RecordIdle records that the agent became idle at now, normally at a turn
// boundary. It resets the idle-age cap so a long-running but recently active
// agent is not mistaken for one idle past maxIdleWarmAge.
func (cw *CacheWarmer) RecordIdle(now time.Time) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.idleSince = now
}

// ShouldWarm reports whether a keepalive replay is due at now. It is safe to
// call concurrently with RecordRequest and RecordIdle. A disabled warmer, one
// that has never seen a request, or one whose economics gate does not clear all
// return false.
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
	// IdleTimeout bounds the silence between stream events. An open connection
	// can stop delivering data without ever reporting an error; with a positive
	// IdleTimeout the read fails as a retryable stream error instead of hanging
	// until the caller's context expires. Zero (the default) keeps synchronous
	// reads and adds no timer or goroutine, so existing callers are unaffected.
	IdleTimeout time.Duration
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
	StopTokenIDs         []int64        `json:"stop_token_ids,omitempty"`
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
	return c.chat(ctx, model, messages, tools, onText, nil)
}

// chat is the shared request/retry/stream pipeline. stopIDs, when non-empty,
// is forwarded as the optional stop_token_ids field; Chat passes nil so its
// wire format is unchanged. FillMiddle uses it to attach FIM stop tokens.
func (c *Client) chat(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
	onText func(string),
	stopIDs []int64,
) (Message, error) {
	cacheEnabled := c.Options.cacheEnabled(ProtocolChatCompletions, c.BaseURL)
	request := chatRequest{
		Model:        model,
		Messages:     wireMessages(messages, cacheEnabled, c.Options.cacheControl()),
		Tools:        tools,
		StopTokenIDs: stopIDs,
		Stream:       true,
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
	// A tool call is keyed by both its stream index and its id. Gateways may
	// renumber a call or swap its id between deltas; both maps alias the same
	// block so every fragment still lands in one place. blocks keeps the
	// distinct calls in creation order for the final result.
	blocks := make([]*ToolCall, 0, 2)
	byIndex := make(map[int]*ToolCall)
	byID := make(map[string]*ToolCall)
	var usage *Usage
	var finishReason string
	var integrity streamIntegrity

	// Reuse the shared SSE reader: a single event may carry its JSON payload
	// across several data: lines, which the SSE spec allows and gateways use.
	reader := newSSEReader(body)
	var pending []string

	// An open connection can stop delivering data without ever reporting an
	// error. With IdleTimeout set, run the normally blocking SSE read in a
	// goroutine and watch it with a timer so silence surfaces as a retryable
	// stream error instead of hanging until the caller's context expires.
	// IdleTimeout == 0 keeps the original synchronous read and adds no timer
	// or goroutine, so existing callers are unaffected.
	type readResult struct {
		data string
		ok   bool
		err  error
	}
	reads := make(chan readResult, 1) // buffered: a late read can always exit
	readStarted := false

	var idle *time.Timer
	var idleC <-chan time.Time
	var resetIdle func()
	if c.IdleTimeout > 0 {
		idle = time.NewTimer(c.IdleTimeout)
		idleC = idle.C
		defer idle.Stop()
		resetIdle = func() {
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(c.IdleTimeout)
		}
	} else {
		resetIdle = func() {}
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
			if idle == nil {
				next, ok, err := reader.next()
				if err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return Message{}, ctxErr
					}
					return Message{}, streamError("read stream: %v", RedactError(err))
				}
				if !ok {
					break streamLoop
				}
				data = next
			} else {
				if !readStarted {
					go func() {
						next, ok, err := reader.next()
						reads <- readResult{data: next, ok: ok, err: err}
					}()
					readStarted = true
				}
				select {
				case <-ctx.Done():
					return Message{}, ctx.Err()
				case <-idleC:
					return Message{}, streamError("stream idle for %s with no data", c.IdleTimeout)
				case res := <-reads:
					readStarted = false
					if res.err != nil {
						if ctxErr := ctx.Err(); ctxErr != nil {
							return Message{}, ctxErr
						}
						return Message{}, streamError("read stream: %v", RedactError(res.err))
					}
					if !res.ok {
						break streamLoop
					}
					data = res.data
				}
			}
			// Any event that arrives pushes the silence deadline out.
			resetIdle()
			// One SSE event may carry several payloads: a JSON object split
			// across data: lines, or independent events with no blank separator.
			// Decode them one at a time through the same path below.
			pending = splitSSEData(data)
			data, pending = pending[0], pending[1:]
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
				// Resolve by index first, then by id: an id may reappear under a
				// new index, or a delta may omit its index after a rename.
				call := byIndex[tc.Index]
				if call == nil && tc.ID != "" {
					call = byID[tc.ID]
				}
				if call == nil {
					call = &ToolCall{Type: "function"}
					blocks = append(blocks, call)
				}
				// Register every key seen for this block as an alias. Later
				// fragments that change either key still resolve back to it.
				if byIndex[tc.Index] == nil {
					byIndex[tc.Index] = call
				}
				if tc.ID != "" {
					if byID[tc.ID] == nil {
						byID[tc.ID] = call
					}
					// Keep the first id: a mid-stream rename is an alias, not
					// a second call, and the agent keys results on that id.
					if call.ID == "" {
						call.ID = tc.ID
					}
				}
				call.Function.Name += tc.Function.Name
				call.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	// Generated ids must not collide with an id the provider supplied: a
	// duplicated call id makes the agent reject the whole tool batch.
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
			for n := i + 1; ; n++ {
				candidate := fmt.Sprintf("call_%d", n)
				if _, taken := used[candidate]; !taken {
					call.ID = candidate
					break
				}
			}
			used[call.ID] = struct{}{}
		}
		// A truncated or malformed argument fragment is not safe to hand to
		// the tool dispatcher; fall back to the empty object like the other
		// protocol clients do. If the stream never reached [DONE], the
		// fallback means the answer was cut off mid-tool-call: fail instead.
		call.Function.Arguments = integrity.toolArguments(call.Function.Arguments)
		calls = append(calls, call)
	}
	if err := integrity.eofError("[DONE]"); err != nil {
		return Message{}, err
	}

	return Message{Role: "assistant", Content: text.String(), ToolCalls: calls, Usage: usage, FinishReason: finishReason}, nil
}
