package security

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 夹具凭据以拼接方式构造，避免秘密扫描器把合成测试值误认为真实密钥。
var (
	toolAPIKeyFixture   = "sk-" + "ant-api03-AbCdEf" + "0123456789xyz"
	toolTokenFixture    = "ghp_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8"
	toolPasswordFixture = "hunter" + "2-example"
	toolJWTFixture      = "eyJ" + "hbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.abc123"
	toolAWSKeyFixture   = "AKIA" + "IOSFODNN7EXAMPLE"
)

// TestRedactToolOutputCredentialShapes 校验进入模型上下文的工具输出中，
// 各种凭据形态都被替换，而周围的说明文字与字段名保留。
func TestRedactToolOutputCredentialShapes(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "api key",
			in:      "loaded key " + toolAPIKeyFixture + " done",
			absent:  []string{toolAPIKeyFixture},
			present: []string{"loaded key", "done", placeholder("api key")},
		},
		{
			name:    "token",
			in:      "token=" + toolTokenFixture,
			absent:  []string{toolTokenFixture},
			present: []string{"token=", placeholder("token")},
		},
		{
			name:    "password",
			in:      "DB_PASSWORD=" + toolPasswordFixture,
			absent:  []string{toolPasswordFixture},
			present: []string{"DB_PASSWORD=", placeholder("password")},
		},
		{
			name:    "jwt bearer",
			in:      "Authorization: Bearer " + toolJWTFixture,
			absent:  []string{toolJWTFixture},
			present: []string{"Authorization:", placeholder("auth header")},
		},
		{
			name:    "aws access key",
			in:      "AWS_ACCESS_KEY_ID=" + toolAWSKeyFixture,
			absent:  []string{toolAWSKeyFixture},
			present: []string{"AWS_ACCESS_KEY_ID=", "[redacted:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := redactToolOutput(tc.in)
			for _, secret := range tc.absent {
				if strings.Contains(out, secret) {
					t.Errorf("redactToolOutput(%q) leaked %q: %q", tc.in, secret, out)
				}
			}
			for _, marker := range tc.present {
				if !strings.Contains(out, marker) {
					t.Errorf("redactToolOutput(%q) lost %q: %q", tc.in, marker, out)
				}
			}
		})
	}
}

// TestRedactToolOutputEnvFile 模拟 `cat .env` 的多行输出：每个凭据形态的
// 环境变量值都必须被脱敏，而非凭据行保持原样。
func TestRedactToolOutputEnvFile(t *testing.T) {
	in := strings.Join([]string{
		"# application config",
		"OPENAI_API_KEY=" + toolAPIKeyFixture,
		"GITHUB_TOKEN=" + toolTokenFixture,
		"DB_PASSWORD=" + toolPasswordFixture,
		"AWS_SECRET_ACCESS_KEY=" + toolAWSKeyFixture,
		"APP_REGION=cn-north-1",
		"DEBUG=true",
	}, "\n")

	out := redactToolOutput(in)
	for _, secret := range []string{
		toolAPIKeyFixture, toolTokenFixture, toolPasswordFixture, toolAWSKeyFixture,
	} {
		if strings.Contains(out, secret) {
			t.Errorf("env output leaked %q: %q", secret, out)
		}
	}
	for _, keep := range []string{"# application config", "APP_REGION=cn-north-1", "DEBUG=true"} {
		if !strings.Contains(out, keep) {
			t.Errorf("env output lost %q: %q", keep, out)
		}
	}
	if n := strings.Count(out, "[redacted:"); n < 4 {
		t.Errorf("got %d redaction placeholders, want at least 4: %q", n, out)
	}
	// 行数必须保持一致：脱敏不应增删整行。
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Errorf("line count changed: in=%q out=%q", in, out)
	}
}

// TestRedactToolOutputMultipleLines 校验单行中多个不同凭据都会被处理，
// 且脱敏不跨越行边界吞掉相邻的正常内容。
func TestRedactToolOutputMultipleLines(t *testing.T) {
	in := "first: api_key=" + toolAPIKeyFixture + "\nsecond: token=" + toolTokenFixture + "\nthird: password=" + toolPasswordFixture + "\nend"
	out := redactToolOutput(in)
	for _, secret := range []string{toolAPIKeyFixture, toolTokenFixture, toolPasswordFixture} {
		if strings.Contains(out, secret) {
			t.Errorf("multi-line output leaked %q: %q", secret, out)
		}
	}
	for _, marker := range []string{"first:", "second:", "third:", "end"} {
		if !strings.Contains(out, marker) {
			t.Errorf("multi-line output lost %q: %q", marker, out)
		}
	}
}

// TestRedactToolOutputKeepsCJKAndEmoji 校验中文与 emoji 在脱敏后完整保留，
// 输出仍是合法 UTF-8。
func TestRedactToolOutputKeepsCJKAndEmoji(t *testing.T) {
	in := "配置加载完成 ✅\nOPENAI_API_KEY=" + toolAPIKeyFixture + "\n中文结尾 😀"
	out := redactToolOutput(in)
	if strings.Contains(out, toolAPIKeyFixture) {
		t.Errorf("api key leaked: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("redacted output is not valid UTF-8: %q", out)
	}
	for _, marker := range []string{"配置加载完成", "✅", "中文结尾", "😀", placeholder("api key")} {
		if !strings.Contains(out, marker) {
			t.Errorf("text %q lost: %q", marker, out)
		}
	}
}

// TestRedactToolOutputNormalContentUnchanged 守护保守性：没有凭据形状时，
// 输出必须与输入逐字节一致；形似凭据的普通文本也不应被误伤。
func TestRedactToolOutputNormalContentUnchanged(t *testing.T) {
	cases := []string{
		"",
		"hello world",
		"user: alice, role: admin",
		"https://example.com/docs/key-management",
		"rotate your api key soon",
		"the PRIVATE KEY text in the docs",
		"中文内容 😀 没有凭据",
		"line1\nline2\nline3",
		"[redacted:api key] already sanitized",
	}
	for _, in := range cases {
		if got := redactToolOutput(in); got != in {
			t.Errorf("redactToolOutput(%q) = %q, want byte-identical input", in, got)
		}
	}
}

// TestRedactToolOutputStripsUnicodeTags 校验工具输出中的不可见 Unicode Tag
// 字符（隐藏提示注入载体）会被剥离，而可见文本不受影响。
func TestRedactToolOutputStripsUnicodeTags(t *testing.T) {
	const hiddenA = '\U000E0041' // Tag 字符，镜像可见的 'A'
	const hiddenB = '\U000E0042' // Tag 字符，镜像可见的 'B'
	in := "hello " + string(hiddenA) + string(hiddenB) + " world"
	out := redactToolOutput(in)
	if strings.ContainsRune(out, hiddenA) || strings.ContainsRune(out, hiddenB) {
		t.Errorf("unicode tags survived: %q", out)
	}
	if out != "hello  world" {
		t.Errorf("redactToolOutput(%q) = %q, want %q", in, out, "hello  world")
	}
}

// TestRedactToolOutputIdempotent 校验重复脱敏是空操作：即使接线处被多次
// 调用，占位符也不会被再次改写。
func TestRedactToolOutputIdempotent(t *testing.T) {
	in := "OPENAI_API_KEY=" + toolAPIKeyFixture + "\n中文 😀 " + toolTokenFixture
	once := redactToolOutput(in)
	if twice := redactToolOutput(once); twice != once {
		t.Errorf("second pass changed output:\n first %q\nsecond %q", once, twice)
	}
}

// TestRedactToolOutputExportedMatchesInternal 锁定导出入口与内部纯函数的
// 一致性，便于将来在 agent 侧 opt-in 接线时直接调用导出版本。
func TestRedactToolOutputExportedMatchesInternal(t *testing.T) {
	in := "DB_PASSWORD=" + toolPasswordFixture + "\n中文 😀"
	if got, want := RedactToolOutput(in), redactToolOutput(in); got != want {
		t.Errorf("RedactToolOutput = %q, want %q", got, want)
	}
}
