package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configShowFixture builds an isolated home + workspace so the layered config
// resolution is fully hermetic: no real HOME, no network, no model.
type configShowFixture struct {
	home string
	work string
	env  map[string]string
}

func newConfigShowFixture(t *testing.T) configShowFixture {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	return configShowFixture{
		home: home,
		work: work,
		env: map[string]string{
			"HOME":        home,
			"USERPROFILE": home,
		},
	}
}

func (f configShowFixture) writeHomeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(f.home, ".argentgo", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f configShowFixture) run(args ...string) (int, string, string) {
	ioState, out, errOut := newTestIO("", f.env)
	full := append([]string{"config", "show", "-C", f.work}, args...)
	return RunWith(App{IO: ioState}, full), out.String(), errOut.String()
}

const configShowFixtureJSON = `{
  // a JSONC comment proves show uses the same loader as the engine
  "model": "file-model",
  "baseURL": "https://file.example/v1",
  "apiKey": "sk-file-secret",
  "max_turns": 9,
  "providers": { "x": { "apiKey": "sk-nested-secret" } },
  "profile": "fast",
  "profiles": { "fast": { "locale": "zh-CN" } }
}`

func TestConfigShowTextLabelsSourcesAndRedacts(t *testing.T) {
	fx := newConfigShowFixture(t)
	fx.writeHomeConfig(t, configShowFixtureJSON)
	fx.env["ARGENT_MODEL"] = "env-model"
	fx.env["ARGENT_API_KEY"] = "sk-env-secret"
	fx.env["ARGENT_PROFILE"] = "fast"

	code, out, errOut := fx.run()
	if code != 0 {
		t.Fatalf("exit = %d, err %q, out %q", code, errOut, out)
	}

	// Effective values: env beats file; a snake_case file key is still "file".
	for _, want := range []string{
		"env-model",
		"[env:ARGENT_MODEL]",
		"https://file.example/v1",
		"[file]",
		"[env:ARGENT_PROFILE]",
		"config sources (lowest to highest precedence):",
		"environment overrides set: ARGENT_API_KEY, ARGENT_MODEL, ARGENT_PROFILE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "file-model") {
		t.Errorf("shadowed file model leaked:\n%s", out)
	}

	// Secrets never appear, but a redaction marker does.
	for _, secret := range []string{"sk-file-secret", "sk-env-secret", "sk-nested-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, configRedacted) {
		t.Errorf("no redaction marker in:\n%s", out)
	}
}

func TestConfigShowJSONRedactsNestedSecretsAndReportsSources(t *testing.T) {
	fx := newConfigShowFixture(t)
	homeCfg := fx.writeHomeConfig(t, configShowFixtureJSON)
	fx.env["ARGENT_MODEL"] = "env-model"

	code, out, _ := fx.run("--json")
	if code != 0 {
		t.Fatalf("exit = %d, out %q", code, out)
	}
	for _, secret := range []string{"sk-file-secret", "sk-nested-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked into JSON:\n%s", secret, out)
		}
	}

	var report struct {
		Values struct {
			Model  string `json:"model"`
			APIKey string `json:"apiKey"`
		} `json:"values"`
		Sources struct {
			Model    string `json:"model"`
			BaseURL  string `json:"baseURL"`
			MaxTurns string `json:"maxTurns"`
		} `json:"sources"`
		ConfigFiles []string `json:"configFiles"`
		Raw         struct {
			APIKey    string `json:"apiKey"`
			Providers map[string]struct {
				APIKey string `json:"apiKey"`
			} `json:"providers"`
		} `json:"raw"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if report.Values.Model != "env-model" || report.Sources.Model != "env:ARGENT_MODEL" {
		t.Errorf("model = %q from %q", report.Values.Model, report.Sources.Model)
	}
	if report.Sources.BaseURL != "file" || report.Sources.MaxTurns != "file" {
		t.Errorf("file sources = %q / %q", report.Sources.BaseURL, report.Sources.MaxTurns)
	}
	if report.Values.APIKey != configRedacted || report.Raw.APIKey != configRedacted {
		t.Errorf("apiKey not redacted: values=%q raw=%q", report.Values.APIKey, report.Raw.APIKey)
	}
	if got := report.Raw.Providers["x"].APIKey; got != configRedacted {
		t.Errorf("nested apiKey = %q, want %q", got, configRedacted)
	}
	if len(report.ConfigFiles) == 0 || report.ConfigFiles[0] != homeCfg {
		t.Errorf("configFiles = %#v, want first = %q", report.ConfigFiles, homeCfg)
	}
}

func TestConfigShowFlagOverrideWinsAndIsLabelled(t *testing.T) {
	fx := newConfigShowFixture(t)
	fx.writeHomeConfig(t, `{"model":"file-model"}`)
	fx.env["ARGENT_MODEL"] = "env-model"

	code, out, _ := fx.run("-c", "model=flag-model", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, out %q", code, out)
	}
	var report struct {
		Values struct {
			Model string `json:"model"`
		} `json:"values"`
		Sources struct {
			Model string `json:"model"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Values.Model != "flag-model" || report.Sources.Model != "flag:model" {
		t.Errorf("model = %q from %q; want flag-model / flag:model", report.Values.Model, report.Sources.Model)
	}
}

func TestConfigShowHelpAndErrors(t *testing.T) {
	fx := newConfigShowFixture(t)

	code, out, errOut := fx.run("--help")
	if code != 0 || !strings.Contains(out, "usage: argent config show") || errOut != "" {
		t.Fatalf("help: exit=%d out=%q err=%q", code, out, errOut)
	}

	code, out, errOut = fx.run("--nope")
	if code != 2 || !strings.Contains(errOut, "unexpected argument") {
		t.Fatalf("unknown flag: exit=%d out=%q err=%q", code, out, errOut)
	}

	code, _, errOut = fx.run("-c", "no-equals")
	if code != 2 || !strings.Contains(errOut, "expected key=value") {
		t.Fatalf("bad override: exit=%d err=%q", code, errOut)
	}

	fx.writeHomeConfig(t, "{not json")
	code, _, errOut = fx.run()
	if code != 1 || !strings.Contains(errOut, "error:") {
		t.Fatalf("invalid config: exit=%d err=%q", code, errOut)
	}
}
