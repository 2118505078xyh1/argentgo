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

	// 带反斜杠的 Windows 路径会破坏 JSON 的原样替换，因此转义重试
	// 必须介入，并仍解码为原始值。
	result := Substitute(`{"path":"{env:WINPATH}"}`, ".", env, nil)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("escaped fallback produced invalid JSON %q: %v", result, err)
	}
	if decoded["path"] != `C:\Users\me` {
		t.Errorf("path = %q, want %q", decoded["path"], `C:\Users\me`)
	}
}

// env 值中的反斜杠即使在恰好构成*合法的* JSON 转义时
// 也必须在替换后保留（"\t"、"\n" 等）。原样结果随后
// 语法上没问题，却解码为另一个字符串（C:\temp -> C:<TAB>emp），
// 因此单靠 JSON 合法性检查无法发现这种损坏。
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

// 真正的 Load 路径上不能出现同样的损坏：apiKey 是带合法
// JSON 转义的 Windows 路径时，配置必须解码为字面的
// 反斜杠序列，而不是制表符/换行。
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

// 当某个 env 值中的反斜杠迫使 Substitute 进行转义重试时，
// 重试不得重新发出原样替换已报告过的警告。缺失的引用
// 每次加载只报告一次，而不是每次替换都报告。
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

// JSONC 注释中的引用是文档，不是设置。旧扫描器只跳过行首
// // 注释中的 file 引用，因此 /* */ 块注释（或行尾 // 注释）
// 中的引用会被读取并注入。
// 当被引用的值包含 "*/" 时，会提前闭合块注释，
// 破坏整个文档。
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
			// 注释在替换后保留，但仍必须被剥离
			// 回严格、可解码的 JSON 文档。
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

// env 值中的双引号可能提前闭合外围的 JSON 字符串，
// 同时让文档保持语法合法。没有转义重试时，apiKey 为
// `a", "role": "admin` 会被悄悄解码为 "a" 加上恶意的
// 键。该值必须逐字节保留，且警告仍必须
// 只触发一次，即使引号迫使转义流程运行。
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

// 真正的 Load 路径上也必须阻止同样的注入：env 值中的
// 引号不得翻转一个无关的安全设置。
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

// 引号只有在 JSON 字符串内才危险。作为原始 JSON 使用的引用
// （此处为对象）必须原样保留，即使同一文档中无关的
// 字符串上下文值触发了转义重试也是如此。
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
