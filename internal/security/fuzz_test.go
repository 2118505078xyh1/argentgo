package security

import (
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 种子语料库由 fuzz 目标和本文件底部的确定性测试共用。因此
// `go test ./internal/security` 恰好覆盖 `go test -fuzz` 运行起始的那些输入，
// 并且无需安装 fuzz 引擎即可在每个种子上验证不变量。
//
// 种子中的凭据在运行时拼装，因此仓库中从不包含会被隐私扫描拒绝的字面量 token。
// 它们全部是由字母 'a' 组成的合成字符串。
func secret(prefix string, n int) string { return prefix + strings.Repeat("a", n) }

var (
	redactSeeds = []string{
		"",
		"plain text without any credential",
		secret("sk-", 32),
		secret("sk-proj-", 28),
		secret("ghp_", 36),
		secret("AKIA", 20),
		"Authorization: Bearer " + secret("sk-", 24),
		"api_key=" + secret("sk-", 24),
		"https://user:" + secret("p", 16) + "@example.com/repo",
		"line1\n" + secret("sk-", 24) + "\nline2",
		"重复 " + secret("sk-", 20) + " 🚀",
		secret("sk-", 20) + "\x00\xff" + secret("ghp_", 24),
	}

	invisibleSeeds = []string{
		"",
		"echo hello",
		"日本語 🚀",
		"\U000e0000", // 最低的 Unicode Tag Block 码位
		"\U000e007f", // 最高的 Unicode Tag Block 码位
		"echo \U000e0041hi",
		"a\u202eb", // bidi 覆盖符
		"\u061c\u200e\u200f\ufeff",
		"\u202a\u202b\u202c\u202d\u202e",
		"\u2066\u2067\u2068\u2069",
		"\U000e0041\u202e\xff\x00", // 混合 tag、bidi 和无效 UTF-8
	}

	globSeeds = [][2]string{
		{"", ""},
		{"*.env", "project/src/.env"},
		{"src/**", "src/a/b.rs"},
		{"~/.ssh", "~/.ssh/id_rsa"},
		{"**/*", "a/b/c"},
		{"read", "read/file.txt"},
		{"a?c", "abc"},
		{"a[bc]d", "abd"},
		{"**\\*", "a\\b\\c"},
		{"[", "abc"}, // 格式错误的模式不得 panic
		{"a*a*a*a*a", "aaaaaaaaaaaa"},
	}

	commandGuardSeeds = []struct{ command, suffix string }{
		{"", ""},
		{"rm -rf x", "safe args"},
		{"git status", ""},
		{"echo \"rm -rf\"", "x"},
		{"cat <<EOF\nrm -rf x\nEOF\n", "y"},
		{"Remove-Item -Force x", ""},
		{"echo hi > /etc/x", ""},
		{"echo hi > /tmp/x", ""},
		{"go test ./...", ""},
		{"ls; rm x", ""},
		{"ls # rm x", ""},
		{"\x00\xffrm\t-rf\nx", "z"},
	}

	wildcardSeeds = [][2]string{
		{"", ""},
		{"read", "read"},
		{"read*", "read/file.txt"},
		{"*.go", "x.go"},
		{"[", "x"}, // 格式错误的模式不得 panic
		{"a?c", "abc"},
		{"**", "a/b"},
	}

	// canonicalSecretPattern 识别高置信度凭据：已知的提供商前缀后跟至少十二个 token
	// 字符。redact.go 中的 tokenEnd 恰好消费这个字符集，因此这里匹配到的内容必须
	// 从 Redact 的输出中消失。
	canonicalSecretPattern = regexp.MustCompile(`(?i)(?:sk-proj-|sk-ant-|sk-or-|sk-|gh[pousr]_|github_pat_|AKIA|ASIA|AIza|xox[abps]-|r8_|hf_|glpat-|npm_|pypi-)[A-Za-z0-9_.~+/=:@%-]{12,}`)
)

// FuzzRedact 检查脱敏的核心承诺：没有高置信度凭据能残留，并且清洗器在任意字节上
// 既不 panic、也不破坏 UTF-8、更不会无界膨胀。
func FuzzRedact(f *testing.F) {
	for _, seed := range redactSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkRedact(t, s)
	})
}

func checkRedact(t *testing.T, s string) {
	t.Helper()
	out := Redact(s)
	if again := Redact(s); again != out {
		t.Fatalf("Redact is not deterministic for %q", s)
	}
	if utf8.ValidString(s) && !utf8.ValidString(out) {
		t.Fatalf("Redact produced invalid UTF-8: in %q out %q", s, out)
	}
	// 每个被接受的匹配至少消费三字节的 token 前缀，并被替换为短于 32 字节的占位符，
	// 因此 16 倍加余量是一个宽松但仍能捕获失控替换循环的上界。
	if len(out) > 16*len(s)+128 {
		t.Fatalf("Redact output length %d is implausible for input length %d", len(out), len(s))
	}
	for _, credential := range canonicalSecretPattern.FindAllString(s, -1) {
		if strings.Contains(out, credential) {
			t.Fatalf("credential %q survived redaction: %q", credential, out)
		}
	}
}

// FuzzSanitizeInvisible 检查提示清洗器移除每个 tag、使每个 bidi/格式 rune 可见，
// 并准确报告 tag。
func FuzzSanitizeInvisible(f *testing.F) {
	for _, seed := range invisibleSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkSanitizeInvisible(t, s)
	})
}

func checkSanitizeInvisible(t *testing.T, s string) {
	t.Helper()
	cleaned, hadTags := SanitizeInvisible(s)
	if again, againTags := SanitizeInvisible(s); again != cleaned || againTags != hadTags {
		t.Fatalf("SanitizeInvisible is not deterministic for %q", s)
	}
	if want := ContainsInvisibleTags(s); hadTags != want {
		t.Fatalf("SanitizeInvisible(%q) hadTags=%v, want %v", s, hadTags, want)
	}
	if utf8.ValidString(s) {
		if !utf8.ValidString(cleaned) {
			t.Fatalf("SanitizeInvisible produced invalid UTF-8: in %q out %q", s, cleaned)
		}
		// 输入有效时，UTF-8 编码是自定界的，因此跳过 tag rune 不会意外地从相邻字节
		// 重新拼出一个 tag。（无效输入则可能：真正的 tag 被移除后，游离的续字节
		// 可能连接成一个 tag 编码。）
		if ContainsInvisibleTags(cleaned) {
			t.Fatalf("a Unicode Tag Block character survived sanitizing %q: %q", s, cleaned)
		}
	}
	for _, r := range s {
		if isBidiOrFormat(r) && strings.ContainsRune(cleaned, r) {
			t.Fatalf("bidi rune U+%04X was not made visible: %q -> %q", r, s, cleaned)
		}
	}
	// 一个最多四字节的 rune 对应的转义是六字节。
	if len(cleaned) > 6*len(s)+16 {
		t.Fatalf("SanitizeInvisible output length %d is implausible for input length %d", len(cleaned), len(s))
	}
}

// FuzzContainsInvisibleTags 把检测器与一个独立参照比较：用 strings.ContainsRune
// 探测 tag 块中的每个码位。
func FuzzContainsInvisibleTags(f *testing.F) {
	for _, seed := range invisibleSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkContainsInvisibleTags(t, s)
	})
}

func checkContainsInvisibleTags(t *testing.T, s string) {
	t.Helper()
	want := false
	for r := rune(tagBlockStart); r <= tagBlockEnd; r++ {
		if strings.ContainsRune(s, r) {
			want = true
			break
		}
	}
	if got := ContainsInvisibleTags(s); got != want {
		t.Fatalf("ContainsInvisibleTags(%q) = %v, want %v", s, got, want)
	}
}

// FuzzEscapeBidi 检查审批提示使用的可见转义辅助函数。
func FuzzEscapeBidi(f *testing.F) {
	for _, seed := range invisibleSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkEscapeBidi(t, s)
	})
}

func checkEscapeBidi(t *testing.T, s string) {
	t.Helper()
	out := EscapeBidi(s)
	if utf8.ValidString(s) && !utf8.ValidString(out) {
		t.Fatalf("EscapeBidi produced invalid UTF-8: in %q out %q", s, out)
	}
	for _, r := range s {
		if isBidiOrFormat(r) && strings.ContainsRune(out, r) {
			t.Fatalf("bidi rune U+%04X was not escaped: %q -> %q", r, s, out)
		}
	}
	if len(out) > 6*len(s)+16 {
		t.Fatalf("EscapeBidi output length %d is implausible for input length %d", len(out), len(s))
	}
}

// FuzzGlobMatch 检查策略路径匹配器是否 panic、不确定，以及其字面量模式（前缀）
// 语义是否退化。Go fuzz 引擎自身的每输入超时会捕获对抗性模式上的回溯爆炸。
func FuzzGlobMatch(f *testing.F) {
	for _, pair := range globSeeds {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		checkGlobMatch(t, pattern, path)
	})
}

func checkGlobMatch(t *testing.T, pattern, path string) {
	t.Helper()
	if got := GlobMatch(pattern, path); got != GlobMatch(pattern, path) {
		t.Fatalf("GlobMatch is not deterministic for (%q, %q)", pattern, path)
	}
	// 不含元字符（也不含 normalize() 会改写的 home 或尾部斜杠）的模式必须匹配其
	// 自身写法，且不得匹配空路径。像 '*' 这样的 glob 元字符合法匹配空串，
	// 因此该规则只适用于字面量模式。
	if pattern != "" && !strings.ContainsAny(pattern, `*?[~\`) && !strings.HasSuffix(pattern, "/") {
		if GlobMatch(pattern, "") {
			t.Fatalf("literal pattern %q matched an empty path", pattern)
		}
		if !GlobMatch(pattern, pattern) {
			t.Fatalf("literal pattern %q does not match itself", pattern)
		}
	}
}

// FuzzFindFileEditingCommand 检查计划模式命令守卫在任意字节上永不 panic，
// 且永不标记只读命令。第二个 fuzz 参数在拼接进 `git status` 之前会被规范化为
// 无作用的参数列表，因此对它的任何改动都保证无害。
func FuzzFindFileEditingCommand(f *testing.F) {
	for _, seed := range commandGuardSeeds {
		f.Add(seed.command, seed.suffix)
	}
	f.Fuzz(func(t *testing.T, command, suffix string) {
		checkCommandGuard(t, command, suffix)
	})
}

func checkCommandGuard(t *testing.T, command, suffix string) {
	t.Helper()
	token := FindFileEditingCommand(command)
	if again := FindFileEditingCommand(command); again != token {
		t.Fatalf("FindFileEditingCommand is not deterministic for %q", command)
	}
	if len(token) > len(command)+2 {
		t.Fatalf("guard returned token %q longer than command %q", token, command)
	}
	if got := FindFileEditingCommand("git status " + safeShellArg(suffix)); got != "" {
		t.Fatalf("read-only command was flagged as mutating: %q -> %q", suffix, got)
	}
}

// safeShellArg 把任意字节映射为只由字母、数字和无害分隔符组成的 shell 词，
// 因此它无法向所拼接的命令引入修改类 token 或重定向。
func safeShellArg(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == ' ', c == '_', c == '-', c == '.', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('x')
		}
	}
	return b.String()
}

// FuzzWildcardMatch 检查权限规则匹配器在格式错误模式上是否 panic，
// 以及字面量模式的自反性。
func FuzzWildcardMatch(f *testing.F) {
	for _, pair := range wildcardSeeds {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, pattern, subject string) {
		checkWildcardMatch(t, pattern, subject)
	})
}

func checkWildcardMatch(t *testing.T, pattern, subject string) {
	t.Helper()
	if got := WildcardMatch(pattern, subject); got != WildcardMatch(pattern, subject) {
		t.Fatalf("WildcardMatch is not deterministic for (%q, %q)", pattern, subject)
	}
	if pattern != "" && !strings.ContainsAny(pattern, `*?[\\`) && !WildcardMatch(pattern, pattern) {
		t.Fatalf("literal pattern %q does not match itself", pattern)
	}
}

// --- 确定性种子测试 ------------------------------------------------
//
// fuzz 目标在 `go test`（不带 -fuzz）期间只运行其种子。这些测试调用相同的不变量
// 辅助函数，因此普通的 `go test ./internal/security` 无需 fuzz 引擎就能守护种子
// 和断言。

func TestRedactSeedCorpus(t *testing.T) {
	for _, seed := range redactSeeds {
		checkRedact(t, seed)
	}
}

func TestSanitizeInvisibleSeedCorpus(t *testing.T) {
	for _, seed := range invisibleSeeds {
		checkSanitizeInvisible(t, seed)
	}
}

func TestContainsInvisibleTagsSeedCorpus(t *testing.T) {
	for _, seed := range invisibleSeeds {
		checkContainsInvisibleTags(t, seed)
	}
}

func TestEscapeBidiSeedCorpus(t *testing.T) {
	for _, seed := range invisibleSeeds {
		checkEscapeBidi(t, seed)
	}
}

func TestGlobMatchSeedCorpus(t *testing.T) {
	for _, pair := range globSeeds {
		checkGlobMatch(t, pair[0], pair[1])
	}
}

func TestCommandGuardSeedCorpus(t *testing.T) {
	for _, seed := range commandGuardSeeds {
		checkCommandGuard(t, seed.command, seed.suffix)
	}
}

func TestWildcardMatchSeedCorpus(t *testing.T) {
	for _, pair := range wildcardSeeds {
		checkWildcardMatch(t, pair[0], pair[1])
	}
}

// TestGlobMatchTerminatesOnBacktrackingPattern 在一个能触发递归 '*' 回溯的小型
// 对抗性输入上限定匹配器。宽松的超时只用于捕获把实际呈线性的遍历变成挂起的
// 退化；它不是性能基准测试。
func TestGlobMatchTerminatesOnBacktrackingPattern(t *testing.T) {
	pattern := "a*a*a*a*a"
	path := strings.Repeat("a", 20) + "b"
	done := make(chan bool, 1)
	go func() { done <- GlobMatch(pattern, path) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GlobMatch did not terminate on a small adversarial pattern")
	}
}
