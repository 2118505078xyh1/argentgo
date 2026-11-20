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
)

// DefaultResponsesMaxOutputTokens is used when ResponsesClient.MaxOutputTokens
// is unset.
const DefaultResponsesMaxOutputTokens = 8192

// ResponsesClient streams the OpenAI Responses protocol (/responses).
//
// It speaks items rather than messages: user/assistant turns keep their role,
// function calls become function_call items, and tool output is returned as a
// function_call_output item keyed by call_id.
type ResponsesClient struct {
	BaseURL         string
	APIKey          string
	MaxOutputTokens int
	HTTP            *http.Client
	Retry           RetryPolicy
	Options         Options
}

// NewResponses returns a Responses client for baseURL (usually ".../v1").
func NewResponses(baseURL, apiKey string) *ResponsesClient {
	return &ResponsesClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{},
	}
}

var _ Chatter = (*ResponsesClient)(nil)
var _ PromptCacheCapable = (*ResponsesClient)(nil)

// PromptCacheCapability reports whether a stable-prefix cache key is emitted.
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

// Chat streams one assistant turn and assembles text, reasoning and calls.
func (c *ResponsesClient) Chat(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolSpec,
	onText func(string),
) (Message, error) {
	if strings.TrimSpace(model) == "" {
		return Message{}, errors.New("responses: model is required")
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
		return c.httpClient().Do(req)
	})
	if err != nil {
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolResponses, model, err))
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		err := providerError(resp)
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(AttemptFailed, ProtocolResponses, model, err))
		return Message{}, err
	}

	reply, err := c.readStream(ctx, resp.Body, onText)
	if err != nil {
		emitAttempt(c.Options.AttemptHook, NewAttemptRecord(ClassifyError(err), ProtocolResponses, model, err))
		return Message{}, err
	}
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
		switch message.Role {
		case "system":
			if strings.TrimSpace(message.Content) != "" {
				instructions = append(instructions, message.Content)
			}
		case "user":
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			req.Input = append(req.Input, responsesTextItem{
				Role:    "user",
				Content: []responsesPart{{Type: "input_text", Text: message.Content}},
			})
		case "assistant":
			if strings.TrimSpace(message.Content) != "" {
				req.Input = append(req.Input, responsesTextItem{
					Role:    "assistant",
					Content: []responsesPart{{Type: "output_text", Text: message.Content}},
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
	Item        *struct {
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
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
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
	reader := newSSEReader(body)

	var (
		text         strings.Builder
		reasoning    strings.Builder
		usage        *Usage
		finishReason string
		order        []int
		integrity    streamIntegrity
		partials     = make(map[int]*responsesPartial)
		// A completed call is parked by its output index instead of being
		// appended right away. output_item.done events can arrive out of the
		// order in which output_item.added announced the items (gateways batch
		// and reorder notifications); emitting on done would silently swap two
		// parallel calls. The final assembly below walks order, which is the
		// provider's announcement sequence, so the call list always matches it.
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
		// An absent provider id stays empty until the final assembly, where a
		// fallback can be chosen against every provider id in the reply.
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
		// An empty data: line is a legal SSE keepalive, not a JSON event.
		// Skipping it keeps a proxy's heartbeat from failing the reply.
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
			if _, exists := partials[event.OutputIndex]; !exists {
				order = append(order, event.OutputIndex)
			}
			partials[event.OutputIndex] = partial
		case "response.function_call_arguments.delta":
			if partial := partials[event.OutputIndex]; partial != nil {
				partial.arguments.WriteString(event.Delta)
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
			if partial.arguments.Len() == 0 {
				partial.arguments.WriteString(event.Item.Arguments)
			}
			push(event.OutputIndex)
		case "response.completed", "response.incomplete":
			if event.Response != nil {
				// Responses reports truncation in incomplete_details.reason
				// (e.g. "max_output_tokens"); fall back to the raw status.
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
				}
				if details := event.Response.Usage.InputTokensDetails; details != nil {
					usage.CachedTokens = details.CachedTokens
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
	// Collect provider ids first: fallback ids are only safe once every real id
	// in the reply is known, otherwise a generated "call_N" can duplicate one.
	used := make(map[string]struct{}, len(order))
	for _, index := range order {
		if call, ok := completed[index]; ok && call.ID != "" {
			used[call.ID] = struct{}{}
		}
	}
	// Assemble in announcement order, not completion order. done handlers may
	// create a partial without a matching added (some gateways only send done),
	// which appends an index a second time; emit each call only once.
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
