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
// Reasoning and Usage are transcript metadata: they are attached to assistant
// replies by the protocol clients so callers can render or persist thinking
// text and token counts. They are never sent back upstream (see Chat).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
}

// Usage reports the tokens a provider billed for one reply.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
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

// Client is a minimal streaming client.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New returns a client for an OpenAI-compatible endpoint.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{},
	}
}

type chatRequest struct {
	Model    string     `json:"model"`
	Messages []Message  `json:"messages"`
	Tools    []ToolSpec `json:"tools,omitempty"`
	Stream   bool       `json:"stream"`
}

// wireMessages copies messages without transcript-only fields, keeping the
// bytes sent upstream exactly as before Reasoning/Usage existed.
func wireMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, m := range messages {
		m.Reasoning = ""
		m.Usage = nil
		out[i] = m
	}
	return out
}

var _ Chatter = (*Client)(nil)

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
	} `json:"choices"`
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
	payload, err := json.Marshal(chatRequest{
		Model:    model,
		Messages: wireMessages(messages),
		Tools:    tools,
		Stream:   true,
	})
	if err != nil {
		return Message{}, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Message{}, fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var text strings.Builder
	order := make([]int, 0, 2)
	partial := make(map[int]*ToolCall)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var event chunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Message{}, fmt.Errorf("decode stream event: %w", err)
		}
		if event.Error != nil {
			return Message{}, fmt.Errorf("provider error: %s", event.Error.Message)
		}
		for _, choice := range event.Choices {
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
		return Message{}, fmt.Errorf("read stream: %w", err)
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

	return Message{Role: "assistant", Content: text.String(), ToolCalls: calls}, nil
}
