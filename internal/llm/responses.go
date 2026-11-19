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

type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []any           `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	Stream          bool            `json:"stream"`
	Store           bool            `json:"store"`
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
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return Message{}, providerError(resp)
	}

	return c.readStream(resp.Body, onText)
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
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
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

func (c *ResponsesClient) readStream(body io.Reader, onText func(string)) (Message, error) {
	reader := newSSEReader(body)

	var (
		text      strings.Builder
		reasoning strings.Builder
		usage     *Usage
		order     []int
		partials  = make(map[int]*responsesPartial)
	)
	var calls []ToolCall

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
		if strings.TrimSpace(arguments) == "" || !json.Valid([]byte(arguments)) {
			arguments = "{}"
		}
		id := partial.id
		if id == "" {
			id = fmt.Sprintf("call_%d", index+1)
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

streamLoop:
	for {
		data, ok, err := reader.next()
		if err != nil {
			return Message{}, fmt.Errorf("read stream: %w", err)
		}
		if !ok {
			break
		}
		if data == "[DONE]" {
			break
		}

		var event responsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Message{}, fmt.Errorf("decode stream event: %w", err)
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
			if event.Response != nil && event.Response.Usage != nil {
				usage = &Usage{
					PromptTokens:     event.Response.Usage.InputTokens,
					CompletionTokens: event.Response.Usage.OutputTokens,
				}
			}
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
			return Message{}, fmt.Errorf("provider error: %s", message)
		}
	}

	for _, index := range order {
		push(index)
	}

	return Message{
		Role:      "assistant",
		Content:   text.String(),
		ToolCalls: calls,
		Reasoning: reasoning.String(),
		Usage:     usage,
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
