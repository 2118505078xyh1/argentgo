package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// `argent auth` reports "configured" for the same provider keys that
// app.Resolve now consumes; the aliases must stay in sync with config.Provider.
func TestConfigProviderKeysMirrorsResolvedKeySources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
		"providers": {
			"openai": {"apiKey": "sk-one"},
			"anthropic": {"api_key": "sk-two"},
			"ollama": {"api-key": "sk-three"},
			"empty": {"apiKey": "   "},
			"missing": {"baseURL": "http://example.test/v1"}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := configProviderKeys(path)
	if err != nil {
		t.Fatalf("configProviderKeys: %v", err)
	}
	for _, name := range []string{"openai", "anthropic", "ollama"} {
		if !got[name] {
			t.Errorf("%s not reported as configured", name)
		}
	}
	for _, name := range []string{"empty", "missing"} {
		if got[name] {
			t.Errorf("%s reported as configured without a key", name)
		}
	}
}
