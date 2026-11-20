package auth

import "strings"

// Provider 是具名的登录预设。Kind 说明凭据如何获得：
// Provider 是具名的登录预设。Kind 说明凭据如何获得：KindAPIKey 表示用户粘贴 key，KindOAuth 使用浏览器流程，设备种类使用 RFC 8628。
// 设备种类使用 RFC 8628。
type Provider struct {
	ID     string
	Name   string
	Kind   string
	Config Config
	// Note 是登录命令显示的简短提示。
	Note string
}

// 设备码种类预设在 Provider.Kind 中报告此值。
const KindDevice = "device"

// presets 是有序的：它是登录命令列出 provider 的顺序。
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

// Presets 按显示顺序列出内置 provider 预设。
func Presets() []Provider {
	out := make([]Provider, len(presets))
	copy(out, presets)
	return out
}

// Preset 按 id（不区分大小写）查找预设。
func Preset(id string) (Provider, bool) {
	wanted := strings.ToLower(strings.TrimSpace(id))
	for _, preset := range presets {
		if preset.ID == wanted {
			return preset, true
		}
	}
	return Provider{}, false
}
