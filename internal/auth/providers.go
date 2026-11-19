package auth

import "strings"

// Provider is a named login preset. Kind says how a credential is obtained:
// KindAPIKey means the user pastes a key, KindOAuth uses the browser flow and
// the device kind uses RFC 8628.
type Provider struct {
	ID     string
	Name   string
	Kind   string
	Config Config
	// Note is a short hint shown by login commands.
	Note string
}

// Kind device-code presets report this in Provider.Kind.
const KindDevice = "device"

// presets is ordered: it is the order login commands should list providers in.
var presets = []Provider{
	{
		ID:   "anthropic",
		Name: "Anthropic",
		Kind: KindAPIKey,
		Config: Config{
			Provider: "anthropic",
		},
		Note: "paste an API key from console.anthropic.com; no browser login",
	},
	{
		ID:   "openai",
		Name: "OpenAI",
		Kind: KindOAuth,
		Config: Config{
			Provider:     "openai",
			ClientID:     "app_EMoamEEZ73f0CkXaXp7hrann",
			AuthorizeURL: "https://auth.openai.com/oauth/authorize",
			TokenURL:     "https://auth.openai.com/oauth/token",
			RedirectURI:  "http://localhost:1455/auth/callback",
			Scopes:       []string{"openid", "profile", "email", "offline_access"},
			Audience:     "https://api.openai.com/v1",
		},
		Note: "browser login with PKCE on loopback port 1455",
	},
	{
		ID:   "github-copilot",
		Name: "GitHub Copilot",
		Kind: KindDevice,
		Config: Config{
			Provider:  "github-copilot",
			ClientID:  "Iv1.b507a08c87ecfe98",
			DeviceURL: "https://github.com/login/device/code",
			TokenURL:  "https://github.com/login/oauth/access_token",
			Scopes:    []string{"read:user"},
		},
		Note: "device flow: open github.com/login/device and enter the code",
	},
	{
		ID:   "custom",
		Name: "Custom OAuth provider",
		Kind: KindOAuth,
		Config: Config{
			Provider:    "custom",
			RedirectURI: "http://127.0.0.1:8765/callback",
		},
		Note: "fill in client id and endpoints yourself",
	},
}

// Presets lists the built-in provider presets in display order.
func Presets() []Provider {
	out := make([]Provider, len(presets))
	copy(out, presets)
	return out
}

// Preset looks up a preset by id (case-insensitive).
func Preset(id string) (Provider, bool) {
	wanted := strings.ToLower(strings.TrimSpace(id))
	for _, preset := range presets {
		if preset.ID == wanted {
			return preset, true
		}
	}
	return Provider{}, false
}
