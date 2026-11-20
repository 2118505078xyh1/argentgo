package security

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 夹具值以拼接方式构造，避免秘密扫描器把合成测试值误认为真实密钥。
var (
	envDumpPlainValue    = "plain-" + "value-123"
	envDumpPasswordValue = "hunter" + "2-env-dump"
	envDumpTokenValue    = "ghp_" + "EnvDumpA1b2C3d4"
)

// TestRedactEnvDumpSensitiveKeys 校验敏感键名的整行值被替换为对应种类的占位符，
// 且原值（即使不具备任何凭据前缀）不再出现在输出里。
func TestRedactEnvDumpSensitiveKeys(t *testing.T) {
	cases := []struct {
		key  string
		kind string
	}{
		{"OPENAI_API_KEY", "api key"},
		{"apikey", "api key"},
		{"GITHUB_TOKEN", "token"},
		{"DB_PASSWORD", "password"},
		{"passwd", "password"},
		{"AWS_SECRET_ACCESS_KEY", "secret"},
		{"PRIVATE_KEY", "api key"},
		{"CLIENT_SECRET", "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			in := tc.key + "=" + envDumpPlainValue
			want := tc.key + "=" + placeholder(tc.kind)
			got := redactEnvDump(in)
			if got != want {
				t.Errorf("redactEnvDump(%q) = %q, want %q", in, got, want)
			}
			if strings.Contains(got, envDumpPlainValue) {
				t.Errorf("redactEnvDump(%q) leaked value %q", in, envDumpPlainValue)
			}
		})
	}
}

// TestRedactEnvDumpNonSensitiveKeys 守护保守性：非敏感键名的行逐字节保持不变。
func TestRedactEnvDumpNonSensitiveKeys(t *testing.T) {
	cases := []string{
		"PATH=/usr/local/bin:/usr/bin",
		"HOME=/home/alice",
		"DEBUG=true",
		"APP_REGION=cn-north-1",
		"LANG=zh_CN.UTF-8",
		"USER=alice",
		"SHELL=/bin/bash",
	}
	for _, in := range cases {
		if got := redactEnvDump(in); got != in {
			t.Errorf("redactEnvDump(%q) = %q, want byte-identical input", in, got)
		}
	}
}

// TestRedactEnvDumpMultipleLines 校验多行 env 输出逐行处理：敏感行被脱敏，
// 非敏感行与注释保持原样，行数不变，脱敏不跨越行边界。
func TestRedactEnvDumpMultipleLines(t *testing.T) {
	in := strings.Join([]string{
		"# application env",
		"OPENAI_API_KEY=" + envDumpPlainValue,
		"APP_REGION=cn-north-1",
		"DB_PASSWORD=" + envDumpPasswordValue,
		"DEBUG=true",
		"GITHUB_TOKEN=" + envDumpTokenValue,
	}, "\n")
	want := strings.Join([]string{
		"# application env",
		"OPENAI_API_KEY=" + placeholder("api key"),
		"APP_REGION=cn-north-1",
		"DB_PASSWORD=" + placeholder("password"),
		"DEBUG=true",
		"GITHUB_TOKEN=" + placeholder("token"),
	}, "\n")
	got := redactEnvDump(in)
	if got != want {
		t.Errorf("redactEnvDump multiline = %q, want %q", got, want)
	}
	if strings.Count(got, "\n") != strings.Count(in, "\n") {
		t.Errorf("line count changed: in=%q out=%q", in, got)
	}
	for _, secret := range []string{envDumpPlainValue, envDumpPasswordValue, envDumpTokenValue} {
		if strings.Contains(got, secret) {
			t.Errorf("multiline output leaked %q: %q", secret, got)
		}
	}
}

// TestRedactEnvDumpCJKValue 校验 CJK 值不会被从多字节字符中间截断：非敏感行的
// 中文与 emoji 原样保留；敏感行的中文值整体被占位符替换，输出仍是合法 UTF-8。
func TestRedactEnvDumpCJKValue(t *testing.T) {
	nonSecret := "WELCOME_MESSAGE=你好，世界 😀"
	if got := redactEnvDump(nonSecret); got != nonSecret {
		t.Errorf("redactEnvDump(%q) = %q, want byte-identical input", nonSecret, got)
	}

	const cjkSecret = "中文口令-密钥-123"
	in := "APP_SECRET=" + cjkSecret
	out := redactEnvDump(in)
	if out != "APP_SECRET="+placeholder("secret") {
		t.Errorf("redactEnvDump(%q) = %q", in, out)
	}
	if strings.Contains(out, cjkSecret) {
		t.Errorf("CJK secret leaked: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("redacted output is not valid UTF-8: %q", out)
	}
}

// TestRedactEnvDumpEmpty 校验空输入返回空输出，空行也原样保留。
func TestRedactEnvDumpEmpty(t *testing.T) {
	if got := redactEnvDump(""); got != "" {
		t.Errorf("redactEnvDump(\"\") = %q, want \"\"", got)
	}
	if got := redactEnvDump("\n\n"); got != "\n\n" {
		t.Errorf("redactEnvDump blank lines = %q, want %q", got, "\n\n")
	}
}

// TestRedactEnvDumpEqualsInValue 校验只按第一个等号切分：值里自带的 `=` 既不
// 改变敏感键名判定，也不会影响非敏感行的逐字节保留。
func TestRedactEnvDumpEqualsInValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "OPENAI_API_KEY=a=b=c",
			want: "OPENAI_API_KEY=" + placeholder("api key"),
		},
		{
			in:   "TOKEN==leading-equals",
			want: "TOKEN=" + placeholder("token"),
		},
		{
			in:   "UNRELATED=a=b=c",
			want: "UNRELATED=a=b=c",
		},
	}
	for _, tc := range cases {
		if got := redactEnvDump(tc.in); got != tc.want {
			t.Errorf("redactEnvDump(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRedactEnvDumpPreservesLineEndings 校验行尾（LF、CRLF、末行无换行）原样保留。
func TestRedactEnvDumpPreservesLineEndings(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "DEBUG=true\r\nDB_PASSWORD=" + envDumpPasswordValue + "\r\n",
			want: "DEBUG=true\r\nDB_PASSWORD=" + placeholder("password") + "\r\n",
		},
		{
			in:   "DEBUG=true\nDB_PASSWORD=" + envDumpPasswordValue,
			want: "DEBUG=true\nDB_PASSWORD=" + placeholder("password"),
		},
		{
			in:   "DB_PASSWORD=" + envDumpPasswordValue + "\n",
			want: "DB_PASSWORD=" + placeholder("password") + "\n",
		},
	}
	for _, tc := range cases {
		if got := redactEnvDump(tc.in); got != tc.want {
			t.Errorf("redactEnvDump(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRedactEnvDumpNonKVLine 校验没有等号或键名为空的行不被改动。
func TestRedactEnvDumpNonKVLine(t *testing.T) {
	for _, in := range []string{"just a log line", "=value", "   ", "no-equals: secret-looking text"} {
		if got := redactEnvDump(in); got != in {
			t.Errorf("redactEnvDump(%q) = %q, want byte-identical input", in, got)
		}
	}
}

// TestRedactEnvDumpIdempotent 校验重复脱敏是空操作，便于接线处多次调用也不改写结果。
func TestRedactEnvDumpIdempotent(t *testing.T) {
	in := "OPENAI_API_KEY=" + envDumpPlainValue + "\n中文 😀\nDB_PASSWORD=" + envDumpPasswordValue
	once := redactEnvDump(in)
	if twice := redactEnvDump(once); twice != once {
		t.Errorf("second pass changed output:\n first %q\nsecond %q", once, twice)
	}
}

// TestRedactEnvDumpExportedMatchesInternal 锁定导出 opt-in 入口与内部纯函数一致，
// 便于将来在调用方启用时直接替换。
func TestRedactEnvDumpExportedMatchesInternal(t *testing.T) {
	in := "DB_PASSWORD=" + envDumpPasswordValue + "\n中文 😀"
	if got, want := RedactEnvDump(in), redactEnvDump(in); got != want {
		t.Errorf("RedactEnvDump = %q, want %q", got, want)
	}
}
