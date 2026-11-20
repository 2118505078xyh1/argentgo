package config

import (
	"path/filepath"
	"testing"
)

func TestProvidersFromReadsAPIKey(t *testing.T) {
	tree := map[string]any{
		"providers": map[string]any{
			"OpenAI": map[string]any{"apiKey": "sk-one"},
			"other":  map[string]any{"api_key": "sk-two"},
			"third":  map[string]any{"api-key": "sk-three"},
		},
	}
	got := providersFrom(tree)
	for name, want := range map[string]string{
		"openai": "sk-one",
		"other":  "sk-two",
		"third":  "sk-three",
	} {
		if entry, ok := got[name]; !ok || entry.APIKey != want {
			t.Errorf("providers[%q] = %+v, want APIKey %q", name, entry, want)
		}
	}
}

func TestProviderOverlayKeepsConfiguredAPIKey(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"providers": map[string]any{
			"openai": map[string]any{
				"apiKey":  "sk-login",
				"baseURL": "http://example.test/v1",
			},
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	provider, ok := cfg.Provider("openai")
	if !ok {
		t.Fatal("openai provider missing")
	}
	if provider.APIKey != "sk-login" {
		t.Errorf("APIKey = %q, want sk-login", provider.APIKey)
	}
	if provider.BaseURL != "http://example.test/v1" {
		t.Errorf("BaseURL = %q, want the configured URL", provider.BaseURL)
	}
	// The login key is an overlay: built-in protocol and env name survive.
	if provider.Protocol != "openai-chat-completions" || provider.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("built-in fields lost: %+v", provider)
	}
}
