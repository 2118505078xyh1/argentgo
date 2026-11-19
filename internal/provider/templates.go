// Package provider holds the built-in provider templates: where each vendor
// lives, which wire protocol it speaks, and which model to use by default.
//
// Templates are data, mirroring crates/provider/providers.json in the Rust
// implementation, but hard-coded so the Go binary needs no asset files. The
// table only describes endpoints; credentials are read from the environment by
// the caller via Template.APIKeyEnv.
package provider

import "strings"

// Protocol names a wire protocol implemented by internal/llm.
type Protocol string

// Supported protocols.
const (
	// ProtocolAnthropic is the Anthropic Messages API (llm.AnthropicClient).
	ProtocolAnthropic Protocol = "anthropic-messages"
	// ProtocolOpenAI is OpenAI chat completions (llm.Client).
	ProtocolOpenAI Protocol = "openai-chat-completions"
	// ProtocolResponses is the OpenAI Responses API (llm.ResponsesClient).
	ProtocolResponses Protocol = "openai-responses"
)

// Template describes one provider endpoint.
type Template struct {
	// Name is the canonical id used by Resolve and on the command line.
	Name string
	// Protocol is the wire protocol to use.
	Protocol Protocol
	// BaseURL is the API root; llm clients append their own path.
	BaseURL string
	// APIKeyEnv names the environment variable holding the key. Empty means
	// the endpoint is normally unauthenticated (local servers).
	APIKeyEnv string
	// DefaultModel is used when the user does not name a model.
	DefaultModel string
}

// templates is the built-in table, ordered as it should be presented to users.
var templates = []Template{
	{
		Name:         "anthropic",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.anthropic.com/v1",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-sonnet-5-5",
	},
	{
		Name:         "openai",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "https://api.openai.com/v1",
		APIKeyEnv:    "OPENAI_API_KEY",
		DefaultModel: "gpt-5.6",
	},
	{
		Name:         "deepseek",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.deepseek.com/anthropic",
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		DefaultModel: "deepseek-v4-pro",
	},
	{
		Name:         "moonshot",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.moonshot.cn/anthropic",
		APIKeyEnv:    "MOONSHOT_API_KEY",
		DefaultModel: "kimi-k3.1-pro",
	},
	{
		Name:         "zhipu",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://open.bigmodel.cn/api/anthropic",
		APIKeyEnv:    "ZHIPU_API_KEY",
		DefaultModel: "GLM-5.3",
	},
	{
		Name:         "qwen",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv:    "DASHSCOPE_API_KEY",
		DefaultModel: "qwen3.8-max",
	},
	{
		Name:         "openrouter",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "https://openrouter.ai/api/v1",
		APIKeyEnv:    "OPENROUTER_API_KEY",
		DefaultModel: "anthropic/claude-sonnet-4.5",
	},
	{
		Name:         "ollama",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "http://127.0.0.1:11434/v1",
		DefaultModel: "llama3.2",
	},
	{
		Name:         "lmstudio",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "http://127.0.0.1:1234/v1",
		DefaultModel: "local-model",
	},
	{
		Name:         "xai",
		Protocol:     ProtocolResponses,
		BaseURL:      "https://api.x.ai/v1",
		APIKeyEnv:    "XAI_API_KEY",
		DefaultModel: "grok-4.7",
	},
	{
		Name:         "zai",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.z.ai/api/anthropic",
		APIKeyEnv:    "ZAI_API_KEY",
		DefaultModel: "GLM-5.3",
	},
	{
		Name:         "bigmodel",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://open.bigmodel.cn/api/anthropic",
		APIKeyEnv:    "BIGMODEL_API_KEY",
		DefaultModel: "GLM-5.3",
	},
	{
		Name:         "minimax",
		Protocol:     ProtocolAnthropic,
		BaseURL:      "https://api.minimaxi.com/anthropic",
		APIKeyEnv:    "MINIMAX_API_KEY",
		DefaultModel: "MiniMax-M3",
	},
	{
		Name:         "local",
		Protocol:     ProtocolOpenAI,
		BaseURL:      "http://127.0.0.1:3425/v1",
		DefaultModel: "auto/deepseek-v4.1-flash",
	},
}

// aliases maps alternate spellings onto canonical names.
var aliases = map[string]string{
	"claude":      "anthropic",
	"kimi":        "moonshot",
	"dashscope":   "qwen",
	"aliyun":      "qwen",
	"z.ai":        "zai",
	"glm":         "zhipu",
	"bigmodel.cn": "zhipu",
	"lm-studio":   "lmstudio",
	"lm_studio":   "lmstudio",
	"open-router": "openrouter",
	"grok":        "xai",
}

// List returns the built-in templates in presentation order. The slice is a
// copy; mutating it does not affect the table.
func List() []Template {
	out := make([]Template, len(templates))
	copy(out, templates)
	return out
}

// Resolve looks up a template by canonical name or alias, case-insensitively.
// The second result reports whether the name was known.
func Resolve(name string) (Template, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := aliases[key]; ok {
		key = canonical
	}
	for _, template := range templates {
		if template.Name == key {
			return template, true
		}
	}
	return Template{}, false
}
