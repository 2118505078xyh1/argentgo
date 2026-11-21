package app

import (
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// chatterFactory 根据 baseURL 与 apiKey 构造某个协议的客户端，并把线协议级选项写入客户端。
type chatterFactory func(base, key string, opts llm.Options) llm.Chatter

// defaultChatterProtocol 是未知协议的回退项在注册表中的键。
const defaultChatterProtocol = "default"

// chatterFactories 是协议名（小写）到构造工厂的注册表。
// 协议通过键查找，因此注册顺序与查找结果无关。
var chatterFactories = make(map[string]chatterFactory)

// registerChatterFactory 把一个协议名绑定到工厂；别名通过多次注册实现。
func registerChatterFactory(protocol string, factory chatterFactory) {
	chatterFactories[protocol] = factory
}

// newAnthropicChatter 构造 Anthropic Messages 客户端，保留原有的默认 max_tokens 设置。
func newAnthropicChatter(base, key string, opts llm.Options) llm.Chatter {
	client := llm.NewAnthropic(base, key)
	client.MaxTokens = llm.DefaultAnthropicMaxTokens
	client.Options = opts
	return client
}

// newResponsesChatter 构造 OpenAI Responses 客户端。
func newResponsesChatter(base, key string, opts llm.Options) llm.Chatter {
	client := llm.NewResponses(base, key)
	client.Options = opts
	return client
}

// newDefaultChatter 构造 OpenAI 兼容的 chat completions 客户端，同时充当未知协议的回退。
func newDefaultChatter(base, key string, opts llm.Options) llm.Chatter {
	client := llm.New(base, key)
	client.Options = opts
	return client
}

func init() {
	// Anthropic Messages 协议及别名。
	registerChatterFactory("anthropic", newAnthropicChatter)
	registerChatterFactory("anthropic-messages", newAnthropicChatter)
	registerChatterFactory("messages", newAnthropicChatter)
	// OpenAI Responses 协议及别名。
	registerChatterFactory("responses", newResponsesChatter)
	registerChatterFactory("openai-responses", newResponsesChatter)
	// OpenAI chat completions 协议，同时作为未知协议的回退。
	registerChatterFactory("default", newDefaultChatter)
	registerChatterFactory("chat", newDefaultChatter)
}
