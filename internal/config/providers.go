package config

import "strings"

// Provider 是一个提供方模板：请求发往何处、使用哪种线
// 协议、哪个环境变量存放密钥，以及未配置任何模型时
// 使用的模型。APIKey 是 `argentgo login` 写入的凭据，
// 位于 providers.<name>.apiKey 下；调用方优先于
// 提供方的环境变量使用它，但仍允许显式 flag 胜出。
type Provider struct {
	Name         string
	BaseURL      string
	Protocol     string
	APIKeyEnv    string
	APIKey       string
	DefaultModel string
}

// builtinProviders 是无需任何配置文件即可用的回退项。
var builtinProviders = map[string]Provider{
	"anthropic": {
		Name:         "anthropic",
		BaseURL:      "https://api.anthropic.com/v1",
		Protocol:     "anthropic-messages",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		DefaultModel: "claude-sonnet-5-5",
	},
	"openai": {
		Name:         "openai",
		BaseURL:      "https://api.openai.com/v1",
		Protocol:     "openai-chat-completions",
		APIKeyEnv:    "OPENAI_API_KEY",
		DefaultModel: "gpt-4o-mini",
	},
	"ollama": {
		Name:         "ollama",
		BaseURL:      "http://localhost:11434/v1",
		Protocol:     "openai-chat-completions",
		DefaultModel: "llama3.2",
	},
}

// Provider 按名称解析提供方模板。空名称使用
// 已配置的提供方，然后是协议天然的提供方。已配置的
// 条目会合并到同名内置项之上，因此配置可以只改
// base URL。
func (c Config) Provider(name string) (Provider, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(c.ProviderName)
	}
	if name == "" {
		name = providerForProtocol(c.Protocol)
	}
	if name == "" {
		return Provider{}, false
	}

	key := strings.ToLower(name)
	builtin, hasBuiltin := builtinProviders[key]
	overlay, hasOverlay := lookupFold(c.Providers, key)
	if !hasBuiltin && !hasOverlay {
		return Provider{}, false
	}

	provider := Provider{}
	if hasBuiltin {
		provider = builtin
	}
	if hasOverlay {
		if overlay.Name != "" {
			provider.Name = overlay.Name
		}
		if overlay.BaseURL != "" {
			provider.BaseURL = overlay.BaseURL
		}
		if overlay.Protocol != "" {
			provider.Protocol = overlay.Protocol
		}
		if overlay.APIKeyEnv != "" {
			provider.APIKeyEnv = overlay.APIKeyEnv
		}
		if overlay.APIKey != "" {
			provider.APIKey = overlay.APIKey
		}
		if overlay.DefaultModel != "" {
			provider.DefaultModel = overlay.DefaultModel
		}
	}
	if provider.Name == "" {
		provider.Name = name
	}
	return provider, true
}

func providerForProtocol(protocol string) string {
	lower := strings.ToLower(protocol)
	switch {
	case strings.Contains(lower, "anthropic"):
		return "anthropic"
	case strings.Contains(lower, "openai"), strings.Contains(lower, "responses"):
		return "openai"
	default:
		return ""
	}
}

// providersFrom 读取 `providers` map，以小写名称为键。
func providersFrom(tree map[string]any) map[string]Provider {
	raw, ok := tree["providers"].(map[string]any)
	if !ok {
		return map[string]Provider{}
	}
	out := make(map[string]Provider, len(raw))
	for name, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		out[strings.ToLower(name)] = Provider{
			Name:         name,
			BaseURL:      stringField(entry, "baseURL", "baseUrl", "base_url"),
			Protocol:     stringField(entry, "protocol"),
			APIKeyEnv:    stringField(entry, "apiKeyEnv", "api_key_env"),
			APIKey:       stringField(entry, "apiKey", "api_key", "api-key"),
			DefaultModel: stringField(entry, "defaultModel", "default_model", "model"),
		}
	}
	return out
}
