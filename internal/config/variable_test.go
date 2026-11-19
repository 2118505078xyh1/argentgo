package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubstituteEnvRawAndEscapedFallback(t *testing.T) {
	env := envFunc(map[string]string{"NAME": "abc", "WINPATH": `C:\Users\me`})

	raw := Substitute(`{"name":"{env:NAME}"}`, ".", env, nil)
	if raw != `{"name":"abc"}` {
		t.Errorf("raw substitution = %q", raw)
	}

	// A backslashed Windows path breaks JSON verbatim, so the escaped retry
	// must kick in and still decode to the original value.
	result := Substitute(`{"path":"{env:WINPATH}"}`, ".", env, nil)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("escaped fallback produced invalid JSON %q: %v", result, err)
	}
	if decoded["path"] != `C:\Users\me` {
		t.Errorf("path = %q, want %q", decoded["path"], `C:\Users\me`)
	}
}

func TestSubstituteFileResolvesRelativeToConfigDir(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := "line one\n\"quoted\"\n"
	if err := os.WriteFile(filepath.Join(confDir, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}

	result := Substitute(`{"key":"{file:secret.txt}"}`, confDir, nil, nil)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("file substitution produced invalid JSON %q: %v", result, err)
	}
	if decoded["key"] != strings.TrimSpace(secret) {
		t.Errorf("key = %q, want %q", decoded["key"], strings.TrimSpace(secret))
	}
}

func TestSubstituteWarnsMissingReferences(t *testing.T) {
	var warnings []string
	warn := func(format string, args ...any) {
		warnings = append(warnings, format)
	}
	result := Substitute(`{"a":"{env:MISSING}","b":"{file:nope.txt}"}`, t.TempDir(),
		envFunc(nil), warn)
	if result != `{"a":"","b":""}` {
		t.Errorf("result = %q, want empty substitutions", result)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %v, want 2", warnings)
	}
}

func TestSubstituteLeavesFileReferenceInCommentAlone(t *testing.T) {
	input := "// {file:missing.txt}\n" + `{"a":"{file:missing.txt}"}`
	var warnings []string
	warn := func(format string, args ...any) { warnings = append(warnings, format) }
	result := Substitute(input, t.TempDir(), nil, warn)
	if strings.Contains(result, "// {file:") == false {
		t.Errorf("comment reference was replaced: %q", result)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want only the real reference", warnings)
	}
}

func TestLoadAppliesSubstitutionAndJSONC(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "api.txt"), []byte("sk-\"secret\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := `{
  // a comment
  "model": "{env:MODEL_NAME}",
  "apiKey": "{file:api.txt}",
  "maxTurns": 7, // trailing comment
}`
	if err := os.WriteFile(filepath.Join(cwd, "argent.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home, "MODEL_NAME": "env-model"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "env-model" {
		t.Errorf("model = %q, want env-model", cfg.Model)
	}
	if cfg.APIKey != `sk-"secret"` {
		t.Errorf("apiKey = %q, want substituted file value", cfg.APIKey)
	}
	if cfg.MaxTurns != 7 {
		t.Errorf("maxTurns = %d, want 7", cfg.MaxTurns)
	}
}
