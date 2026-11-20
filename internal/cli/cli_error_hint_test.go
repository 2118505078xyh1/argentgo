package cli

import (
	"strings"
	"testing"
)

// TestCliErrorHintByBranch 覆盖四个失败分支，确认每类错误信息都返回专属的
// 中文修复建议片段（而不是笼统的一条提示）。
func TestCliErrorHintByBranch(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string // 期望提示中包含的关键片段
	}{
		// 凭证 / token。
		{"missing token", "missing token: set ARGENT_API_KEY", "缺少或无效的凭证"},
		{"api key", "provider not configured: no api key found", "缺少或无效的凭证"},
		{"unauthorized 401", "provider returned 401 Unauthorized", "缺少或无效的凭证"},
		{"中文未配置", "提供方未配置凭证", "缺少或无效的凭证"},
		// TLS / 证书。
		{"tls handshake", "tls: handshake failed", "TLS/证书校验失败"},
		{"x509 certificate", "x509: certificate signed by unknown authority", "TLS/证书校验失败"},
		{"ssl", "ssl connect error", "TLS/证书校验失败"},
		{"中文证书", "证书校验失败：自签名证书不被信任", "TLS/证书校验失败"},
		// 文件不存在。
		{"no such file", "open config.json: no such file or directory", "文件不存在"},
		{"file not found", "file not found: skills/SKILL.md", "文件不存在"},
		{"does not exist", "the path does not exist", "文件不存在"},
		{"中文文件不存在", "文件不存在：请先创建该文件", "文件不存在"},
		// 非交互。
		{"non-interactive", "cannot prompt in non-interactive mode", "非交互环境"},
		{"not a tty", "stdin is not a tty", "非交互环境"},
		{"中文非交互", "非交互环境无法读取输入", "非交互环境"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cliErrorHint(tc.msg)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("cliErrorHint(%q) = %q, want to contain %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestCliErrorHintCaseInsensitive 确认 ASCII 关键词按不区分大小写的方式匹配，
// 大写或混合大小写的错误信息仍被正确归类。
func TestCliErrorHintCaseInsensitive(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"TLS: HANDSHAKE FAILED", "TLS/证书校验失败"},
		{"No Such File Or Directory", "文件不存在"},
		{"NON-INTERACTIVE MODE", "非交互环境"},
		{"Invalid TOKEN / UnAuthorized", "缺少或无效的凭证"},
	}
	for _, tc := range cases {
		got := cliErrorHint(tc.msg)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("cliErrorHint(%q) = %q, want to contain %q", tc.msg, got, tc.want)
		}
	}
}

// TestCliErrorHintPriority 确认一条命中多个分支的信息取优先级最高的分支：
// 具体的环境问题（TLS、文件、终端）优先于通用的凭证问题。
func TestCliErrorHintPriority(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"tls certificate file: no such file or directory", "TLS/证书校验失败"},
		{"401 unauthorized but token valid", "缺少或无效的凭证"},
	}
	for _, tc := range cases {
		got := cliErrorHint(tc.msg)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("cliErrorHint(%q) = %q, want to contain %q", tc.msg, got, tc.want)
		}
	}
}

// TestCliErrorHintDefaultEmpty 确认空输入与未命中任何关键词的错误信息返回空串，
// 不产生任何默认提示。
func TestCliErrorHintDefaultEmpty(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"something unexpected happened",
		"provider returned 500 Internal Server Error",
		"rate limit exceeded, retry later",
	}
	for _, msg := range cases {
		if got := cliErrorHint(msg); got != "" {
			t.Fatalf("cliErrorHint(%q) = %q, want empty", msg, got)
		}
	}
}

// TestCliErrorHintBounded 确认超出扫描上限后出现的关键词不会被匹配，
// 即对超长输入是有界扫描，而不是扫描整条字符串。
func TestCliErrorHintBounded(t *testing.T) {
	msg := strings.Repeat("占", cliErrorHintMaxRunes+10) + " TLS certificate"
	if got := cliErrorHint(msg); got != "" {
		t.Fatalf("keyword beyond scan bound was matched: %q", got)
	}
}

// TestCliErrorHintCJKSafeAtBoundary 确认在扫描上限边界处的多字节 CJK 字符
// 不会被切断：紧邻边界的中文关键词仍能被识别，且结果不含 U+FFFD 替换字符。
func TestCliErrorHintCJKSafeAtBoundary(t *testing.T) {
	// 前缀占满到上限前两个码点，随后紧跟中文关键词“证书”。
	msg := strings.Repeat("中", cliErrorHintMaxRunes-2) + "证书校验失败"
	got := cliErrorHint(msg)
	if !strings.Contains(got, "TLS/证书校验失败") {
		t.Fatalf("CJK keyword at rune boundary not matched: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("advice contains replacement rune (broken UTF-8): %q", got)
	}
}

// TestCliErrorHintOutputIndependentOfInput 确认返回值只由命中的固定文案决定，
// 不会把原始错误信息（可能含敏感内容）嵌入提示，长度也与输入无关。
func TestCliErrorHintOutputIndependentOfInput(t *testing.T) {
	base := cliErrorHint("token missing")
	if base == "" {
		t.Fatal("base hint unexpectedly empty")
	}
	long := cliErrorHint("token missing" + strings.Repeat("secret-xyz", 50))
	if long != base {
		t.Fatalf("hint depends on input content/length:\nbase = %q\nlong = %q", base, long)
	}
}
