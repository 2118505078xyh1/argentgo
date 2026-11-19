package app

import (
	"context"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func newChatter(protocol, baseURL, apiKey string, opts llm.Options) llm.Chatter {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic", "anthropic-messages", "messages":
		client := llm.NewAnthropic(baseURL, apiKey)
		client.MaxTokens = llm.DefaultAnthropicMaxTokens
		client.Options = opts
		return client
	case "responses", "openai-responses":
		client := llm.NewResponses(baseURL, apiKey)
		client.Options = opts
		return client
	default:
		client := llm.New(baseURL, apiKey)
		client.Options = opts
		return client
	}
}

// fallbackChatter retries once with --fallback-model when the primary model
// fails. Cancellation is never retried.
type fallbackChatter struct {
	primary  llm.Chatter
	fallback string
}

func (f *fallbackChatter) Chat(
	ctx context.Context,
	model string,
	messages []llm.Message,
	tools []llm.ToolSpec,
	onText func(string),
) (llm.Message, error) {
	reply, err := f.primary.Chat(ctx, model, messages, tools, onText)
	if err == nil || ctx.Err() != nil || strings.TrimSpace(f.fallback) == "" {
		return reply, err
	}
	return f.primary.Chat(ctx, f.fallback, messages, tools, onText)
}
