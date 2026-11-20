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

// A backslash in an env value must survive substitution even when it happens
// to form a *valid* JSON escape ("\t", "\n", ...). The verbatim result is then
// syntactically fine but decodes to a different string (C:\temp -> C:<TAB>emp),
// so the JSON-validity check alone cannot detect the corruption.
func TestSubstituteBackslashValueThatIsValidJSONEscape(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"windows temp path", `C:\temp`},
		{"literal newline escape", `a\nb`},
		{"literal backspace escape", `C:\bin`},
		{"invalid escape still works", `C:\Users\me`},
		{"plain value stays verbatim", `abc`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Substitute(`{"v":"{env:X}"}`, ".", envFunc(map[string]string{"X": tc.value}), nil)
			var decoded map[string]string
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("substituted JSON %q is invalid: %v", got, err)
			}
			if decoded["v"] != tc.value {
				t.Fatalf("value = %q, want %q (substituted text %q)", decoded["v"], tc.value, got)
			}
		})
	}
}

// The same corruption must not happen on the real Load path: a config whose
// apiKey is a Windows path with a valid JSON escape must decode to the literal
// backslash sequence, not to a tab/newline.
func TestLoadPreservesEnvBackslashValue(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	content := `{"apiKey": "{env:KEY}"}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = `C:\temp\new`
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home, "KEY": want}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != want {
		t.Fatalf("apiKey = %q, want %q", cfg.APIKey, want)
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

// When a backslash in one env value forces Substitute's escaped retry, the
// retry must not re-emit warnings already reported by the verbatim pass. A
// missing reference is reported once per load, not once per substitution pass.
func TestSubstituteWarnsOnceWhenEscapedFallbackRuns(t *testing.T) {
	env := envFunc(map[string]string{"WINPATH": `C:\temp`})
	var warnings []string
	warn := func(format string, args ...any) { warnings = append(warnings, format) }

	input := `{"path":"{env:WINPATH}","missingEnv":"{env:ABSENT}","missingFile":"{file:absent.txt}"}`
	got := Substitute(input, t.TempDir(), env, warn)

	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("substituted JSON %q is invalid: %v", got, err)
	}
	if decoded["path"] != `C:\temp` {
		t.Errorf("path = %q, want %q", decoded["path"], `C:\temp`)
	}

	const envFormat = "config: environment variable %q referenced in config is not set"
	const fileFormat = "config: file reference %q (%s) could not be read: %v"
	var envWarnings, fileWarnings int
	for _, message := range warnings {
		switch message {
		case envFormat:
			envWarnings++
		case fileFormat:
			fileWarnings++
		}
	}
	if envWarnings != 1 || fileWarnings != 1 {
		t.Fatalf("warnings = %v, want exactly one env and one file warning (got %d env, %d file)",
			warnings, envWarnings, fileWarnings)
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

// A double quote in an env value can close the surrounding JSON string early
// while leaving the document syntactically valid. Without an escaping retry,
// an apiKey of `a", "role": "admin` would silently decode as "a" plus a rogue
// key. The value must survive byte-for-byte and the warning must still fire
// only once even though the quote forces the escaping pass to run.
func TestSubstituteEnvQuoteInsideStringCannotInjectJSON(t *testing.T) {
	const payload = `a", "injected": "pwned`
	env := envFunc(map[string]string{"VAL": payload})
	var warnings []string
	warn := func(format string, args ...any) { warnings = append(warnings, format) }

	got := Substitute(`{"model":"{env:VAL}","missing":"{env:ABSENT}"}`, ".", env, warn)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("substituted JSON %q is invalid: %v", got, err)
	}
	if decoded["model"] != payload {
		t.Fatalf("model = %q, want %q (substituted text %q)", decoded["model"], payload, got)
	}
	if _, ok := decoded["injected"]; ok {
		t.Fatalf("env value injected an extra key: %q", got)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want exactly 1 despite the escaping retry", warnings)
	}
}

// The same injection must be stopped on the real Load path: a quote in an env
// value must not be able to flip an unrelated security setting.
func TestLoadPreservesEnvQuoteInStringValue(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	if err := os.WriteFile(path, []byte(`{"apiKey": "{env:KEY}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = `sk-", "sandboxMode": "danger-full-access`
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home, "KEY": want}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != want {
		t.Fatalf("apiKey = %q, want %q", cfg.APIKey, want)
	}
	if cfg.SandboxMode != SandboxWorkspaceWrite {
		t.Fatalf("sandboxMode = %q, want the default; env value injected a key", cfg.SandboxMode)
	}
}

// Quotes are only dangerous inside a JSON string. A reference used as raw JSON
// (an object here) must stay verbatim, including when an escaping retry is
// triggered by an unrelated string-context value in the same document.
func TestSubstituteEnvQuoteOutsideStringStaysRawJSON(t *testing.T) {
	const obj = `{"a":1}`
	env := envFunc(map[string]string{"OBJ": obj, "Q": `a", "x": "y`})

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"alone", `{"extra":{env:OBJ}}`},
		{"alongside a string quote", `{"model":"{env:Q}","extra":{env:OBJ}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Substitute(tc.input, ".", env, nil)
			var decoded map[string]any
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("substituted JSON %q is invalid: %v", got, err)
			}
			extra, ok := decoded["extra"].(map[string]any)
			if !ok || extra["a"] != float64(1) {
				t.Fatalf("extra = %#v, want the injected raw object", decoded["extra"])
			}
		})
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
