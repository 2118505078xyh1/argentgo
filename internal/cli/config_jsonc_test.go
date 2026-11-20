package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Subcommands share loadConfigFile with the main config loader, so a
// hand-edited JSONC file must work everywhere, not only on the engine path.
func TestLoadConfigFileAcceptsJSONC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
		// model overrides the compiled default
		"model": "comment-model", // trailing line comment
		/* block comment containing "quotes" and http:// */
		"baseURL": "http://example.test/v1",
		"providers": {
			"openai": {"apiKey": "sk-jsonc",}, // trailing comma
		},
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if got := cfg["model"]; got != "comment-model" {
		t.Errorf("model = %#v", got)
	}
	if got := cfg["baseURL"]; got != "http://example.test/v1" {
		t.Errorf("baseURL = %#v (comment-like text in a string must survive)", got)
	}
	providers, _ := cfg["providers"].(map[string]any)
	openai, _ := providers["openai"].(map[string]any)
	if got := openai["apiKey"]; got != "sk-jsonc" {
		t.Errorf("providers.openai.apiKey = %#v", got)
	}

	// auth's provider listing goes through the same loader.
	keys, err := configProviderKeys(path)
	if err != nil {
		t.Fatalf("configProviderKeys: %v", err)
	}
	if !keys["openai"] {
		t.Error("openai key not reported by auth path")
	}
}

func TestConfigGetReadsJSONCFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := "{\n  // picked by the user\n  \"model\": \"jsonc-model\",\n}"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: dir}, []string{"config", "get", "model"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	if got := out.String(); got != "jsonc-model\n" {
		t.Fatalf("out = %q", got)
	}
}

func TestConfigSetWarnsBeforeDroppingJSONC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := "{\n  // keep me if you can\n  \"model\": \"old\",\n}"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	stdio, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: dir}, []string{"config", "set", "model", "new"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	warning := errOut.String()
	if !strings.Contains(warning, "warning") || !strings.Contains(warning, "strict JSON") {
		t.Fatalf("missing rewrite warning: %q", warning)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "keep me") {
		t.Fatalf("comment survived rewrite: %q", written)
	}
	cfg, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("load rewritten file: %v", err)
	}
	if got := cfg["model"]; got != "new" {
		t.Fatalf("model = %#v", got)
	}
}

func TestConfigSetDoesNotWarnForStrictJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{\n  \"model\": \"old\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdio, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: dir}, []string{"config", "set", "model", "new"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	if got := errOut.String(); got != "" {
		t.Fatalf("unexpected warning for strict JSON: %q", got)
	}
}
