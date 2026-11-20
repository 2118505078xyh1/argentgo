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

// DefaultAnthropicMaxTokens is used when AnthropicClient.MaxTokens is unset.
// The Messages API requires max_tokens on every request.
const DefaultAnthropicMaxTokens = 8192

// AnthropicVersion is the wire revision sent in anthropic-version.
const AnthropicVersion = "2023-06-01"

// AnthropicClient streams the Anthropic Messages protocol.
//
// Tool calls arrive as tool_use content blocks whose arguments are assembled
// from input_json_delta fragments; tool results are sent back on the next
// request as tool_result blocks inside a user message.
type AnthropicClient struct {
	BaseURL   string
	APIKey    string
	Version   string
	MaxTokens int
	HTTP      *http.Client
	Retry     RetryPolicy
	Options   Options
}

// NewAnthropic returns a Messages client for baseURL (usually ".../v1").
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

// PromptCacheCapability reports whether stable-prefix markers are emitted.
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
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
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

// Chat streams one assistant turn and assembles text, thinking and tool calls.
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

// buildRequest converts neutral messages into Messages wire blocks. System
// prompts are hoisted out; adjacent same-role turns are merged, which is what
// keeps a batch of tool_result blocks in one user message.
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
			blocks := textBlocks(message.Content)
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
			// The system prompt and tool schemas form the stable prefix; the
			// conversation tail gets its own advancing breakpoint below.
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
		// One breakpoint at the end of the tool prefix covers all schemas.
		req.Tools[len(req.Tools)-1].CacheControl = c.Options.cacheControl()
	}
	if cacheEnabled {
		// Conversation-tail breakpoint: anchor the cache at the final tool
		// result so the cached prefix advances after each completed batch of
		// tool calls. Tool turns are merged into user messages, so the marker
		// belongs on the block, not on the message itself.
		if block := lastToolResultBlock(req.Messages); block != nil {
			block.CacheControl = c.Options.normalizedCacheControl()
		}
	}
	return req
}

// lastToolResultBlock returns the final tool_result content block in the wire
// messages, or nil when the request contains no tool results. Scanning from
// the end keeps only one breakpoint on the newest tail.
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
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
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
	reader := newSSEReader(body)

	var (
		text      strings.Builder
		reasoning strings.Builder
		usage     *Usage
		// baseInput is message_start's bare input_tokens (cache excluded).
		// message_delta carries the authoritative cache counts, so PromptTokens
		// is rebuilt from this base instead of summing the two snapshots.
		baseInput    int
		finishReason string
		integrity    streamIntegrity
		partials     = make(map[int]*anthropicPartial)
	)
	var calls []ToolCall

streamLoop:
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
				partials[event.Index] = partial
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
				// Signatures are not part of the neutral Message contract;
				// thinking text is transcript-only here.
			case "input_json_delta":
				if partial := partials[event.Index]; partial != nil {
					partial.arguments.WriteString(delta.PartialJSON)
				}
			}
		case "content_block_stop":
			partial, ok := partials[event.Index]
			if !ok {
				continue
			}
			delete(partials, event.Index)
			if strings.TrimSpace(partial.name) == "" {
				continue
			}
			arguments := partial.arguments.String()
			if strings.TrimSpace(arguments) == "" {
				arguments = partial.start
			}
			arguments = integrity.toolArguments(arguments)
			id := partial.id
			if id == "" {
				id = fmt.Sprintf("toolu_%d", event.Index)
			}
			calls = append(calls, ToolCall{
				ID:   id,
				Type: "function",
				Function: ToolCallFunction{
					Name:      partial.name,
					Arguments: arguments,
				},
			})
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
				// Cache counts in message_delta are final cumulative values, not
				// increments. Assign (never add) so a cache count also present in
				// message_start is not counted twice.
				if event.Usage.CacheReadInputTokens > 0 {
					usage.CachedTokens = event.Usage.CacheReadInputTokens
				}
				if event.Usage.CacheCreationInputTokens > 0 {
					usage.CacheWriteTokens = event.Usage.CacheCreationInputTokens
				}
				// PromptTokens is the full prompt cost (fresh + cached). Rebuild it
				// from message_start's bare input plus the authoritative delta cache
				// counts; otherwise CachedTokens can exceed PromptTokens and budget
				// accounting undercounts.
				usage.PromptTokens = baseInput + usage.CachedTokens + usage.CacheWriteTokens
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

	// Gateways that omit content_block_stop still hand back complete calls.
	for _, index := range sortedIndices(partials) {
		partial := partials[index]
		arguments := partial.arguments.String()
		if strings.TrimSpace(arguments) == "" {
			arguments = partial.start
		}
		arguments = integrity.toolArguments(arguments)
		id := partial.id
		if id == "" {
			id = fmt.Sprintf("toolu_%d", index)
		}
		calls = append(calls, ToolCall{
			ID:   id,
			Type: "function",
			Function: ToolCallFunction{
				Name:      partial.name,
				Arguments: arguments,
			},
		})
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
	return &Usage{
		PromptTokens:     usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens,
		CompletionTokens: usage.OutputTokens,
		CachedTokens:     usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
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

// sseReader turns a response body into Server-Sent Events payloads. Multi-line
// data fields are joined with newlines, comments and non-data fields are
// skipped, and a blank line terminates an event.
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
