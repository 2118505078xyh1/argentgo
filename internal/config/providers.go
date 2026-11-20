package config

import "strings"

// Provider is one provider template: where to send requests, which wire
// protocol to speak, which environment variable holds the key, and the model
// used when none is configured. APIKey is the credential written by
// `argent login` under providers.<name>.apiKey; callers prefer it over the
// provider's environment variable but still allow an explicit flag to win.
type Provider struct {
	Name         string
	BaseURL      string
	Protocol     string
	APIKeyEnv    string
	APIKey       string
	DefaultModel string
}

// builtinProviders are the fallbacks available without any config file.
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

// Provider resolves a provider template by name. An empty name uses the
// configured provider, then the protocol's natural provider. A configured
// entry is merged over the built-in of the same name, so a config can change
// only the base URL.
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

// providersFrom reads the `providers` map, keyed by lower-case name.
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
