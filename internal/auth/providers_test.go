package auth

import (
	"net/url"
	"strings"
	"testing"
)

func TestPresetsCoverTheDocumentedProviders(t *testing.T) {
	ids := []string{"anthropic", "openai", "github-copilot", "custom"}
	for _, id := range ids {
		preset, ok := Preset(id)
		if !ok {
			t.Fatalf("preset %q missing", id)
		}
		if preset.Name == "" || preset.Note == "" {
			t.Fatalf("preset %q is incomplete: %+v", id, preset)
		}
		if preset.Config.Provider != id {
			t.Fatalf("preset %q stores provider %q", id, preset.Config.Provider)
		}
	}
	if len(Presets()) != len(ids) {
		t.Fatalf("Presets() returned %d entries, want %d", len(Presets()), len(ids))
	}
	if _, ok := Preset("nope"); ok {
		t.Fatal("Preset found an unknown provider")
	}
	if preset, ok := Preset("  OpenAI "); !ok || preset.ID != "openai" {
		t.Fatal("Preset is not case-insensitive")
	}
}

func TestAnthropicIsAPIKeyOnly(t *testing.T) {
	preset, ok := Preset("anthropic")
	if !ok {
		t.Fatal("anthropic preset missing")
	}
	if preset.Kind != KindAPIKey {
		t.Fatalf("kind = %q, want %q", preset.Kind, KindAPIKey)
	}
	if preset.Config.AuthorizeURL != "" || preset.Config.TokenURL != "" || preset.Config.DeviceURL != "" {
		t.Fatalf("API-key preset has OAuth endpoints: %+v", preset.Config)
	}
}

func TestOpenAIPresetBuildsABrowserURL(t *testing.T) {
	preset, _ := Preset("openai")
	if preset.Kind != KindOAuth {
		t.Fatalf("kind = %q, want oauth", preset.Kind)
	}
	raw, err := AuthorizeURL(preset.Config, "state", "challenge")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Host != "auth.openai.com" {
		t.Fatalf("authorize host = %q", parsed.Host)
	}
	if !strings.Contains(parsed.Query().Get("scope"), "offline_access") {
		t.Fatalf("scope = %q", parsed.Query().Get("scope"))
	}
	if parsed.Query().Get("redirect_uri") != preset.Config.RedirectURI {
		t.Fatalf("redirect_uri = %q", parsed.Query().Get("redirect_uri"))
	}
}

func TestCopilotPresetUsesTheDeviceFlow(t *testing.T) {
	preset, _ := Preset("github-copilot")
	if preset.Kind != KindDevice {
		t.Fatalf("kind = %q, want device", preset.Kind)
	}
	if preset.Config.DeviceURL == "" || preset.Config.TokenURL == "" {
		t.Fatalf("device preset is missing endpoints: %+v", preset.Config)
	}
	if preset.Config.AuthorizeURL != "" {
		t.Fatal("device preset also advertises a browser flow")
	}
}

func TestCustomPresetHasALoopbackRedirect(t *testing.T) {
	preset, _ := Preset("custom")
	if !strings.HasPrefix(preset.Config.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("redirect uri = %q", preset.Config.RedirectURI)
	}
	if preset.Config.AuthorizeURL != "" || preset.Config.TokenURL != "" {
		t.Fatal("the custom preset should leave the endpoints to the caller")
	}
}

func TestPresetsReturnsACopy(t *testing.T) {
	first := Presets()
	first[0].ID = "mutated"
	second := Presets()
	if second[0].ID == "mutated" {
		t.Fatal("Presets leaked its internal slice")
	}
}
