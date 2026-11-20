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

// A reference inside a JSONC comment is documentation, not a setting. The old
// scanner only skipped file refs in // comments that began a line, so a ref in
// a /* */ block comment (or in a trailing // comment) was read and injected.
// When the referenced value contained "*/" it closed the block comment early
// and corrupted the whole document.
func TestSubstituteIgnoresReferencesInComments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.txt"), []byte("abc*/def"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := envFunc(map[string]string{"BAD": "x*/y", "GOOD": "value"})

	cases := []struct {
		name     string
		input    string
		want     string
		wantWarn int
	}{
		{
			name:  "file ref in block comment with closing marker in file",
			input: "{\n  /* key: {file:key.txt} */\n  \"model\": \"m\"\n}",
			want:  "{\n  /* key: {file:key.txt} */\n  \"model\": \"m\"\n}",
		},
		{
			name:  "env ref in block comment with closing marker in value",
			input: "{\n  /* note: {env:BAD} */\n  \"model\": \"m\"\n}",
			want:  "{\n  /* note: {env:BAD} */\n  \"model\": \"m\"\n}",
		},
		{
			name:  "missing env ref in line comment does not warn",
			input: "// note {env:MISSING}\n{\"model\":\"m\"}",
			want:  "// note {env:MISSING}\n{\"model\":\"m\"}",
		},
		{
			name:  "file ref in trailing line comment after a value",
			input: "{\"model\":\"m\", // {file:absent.txt}\n \"x\":1}",
			want:  "{\"model\":\"m\", // {file:absent.txt}\n \"x\":1}",
		},
		{
			name:  "real file ref inside a string value is still substituted",
			input: `{"key":"{file:key.txt}"}`,
			want:  `{"key":"abc*/def"}`,
		},
		{
			name:  "real env ref inside a string value is still substituted",
			input: `{"model":"{env:GOOD}"}`,
			want:  `{"model":"value"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var warnings []string
			warn := func(format string, args ...any) { warnings = append(warnings, format) }
			got := Substitute(tc.input, dir, env, warn)
			if got != tc.want {
				t.Fatalf("Substitute() = %q, want %q", got, tc.want)
			}
			if len(warnings) != tc.wantWarn {
				t.Errorf("warnings = %v, want %d", warnings, tc.wantWarn)
			}
			// Comments survive substitution but must still be stripped back
			// into a strict, decodable JSON document.
			var tree map[string]any
			if err := json.Unmarshal([]byte(StripJSONC(got)), &tree); err != nil {
				t.Fatalf("cleaned JSON is invalid: %v\n%s", err, StripJSONC(got))
			}
		})
	}
}

func TestLoadBlockCommentFileRefDoesNotCorruptJSON(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "key.txt"), []byte("sk-*/secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "{\n  /* key loaded from {file:key.txt} */\n  \"model\": \"comment-model\",\n}\n"
	if err := os.WriteFile(filepath.Join(cwd, "argent.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load with commented file reference: %v", err)
	}
	if cfg.Model != "comment-model" {
		t.Errorf("model = %q, want comment-model", cfg.Model)
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
