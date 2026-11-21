package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestLooksEncodedPayload 覆盖函数承诺识别的各编码形态，以及必须放行的良性输入。
func TestLooksEncodedPayload(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"empty", "", false},
		{"plain ascii", "echo hello", false},
		{"cjk literal stays safe", "echo 中文报告 日本語", false},
		{"cjk utf8 bytes stay safe", "文档/报告.txt", false},

		// base64：短串与解码后非文本的字母串都不算，长串且解码为可打印文本才算。
		{"base64 too short", "YmFzaA==", false},
		{"base64 of short command too short", "cm0gLXJmIC8=", false},
		{"base64 of shell command", "YmFzaCAtYyAncm0gLXJmIC8n", true},
		{"base64 with padding", "ZWNobyBoZWxsbyB3b3JsZA==", true},
		{"base64 embedded in a command", "echo YmFzaCAtYyAncm0gLXJmIC8n | base64 -d", true},
		{"long plain word is not base64 text", strings.Repeat("x", 64), false},
		{"long repeated letter is not base64 text", strings.Repeat("a", 64), false},
		{"long identifier with separators", "my-long_file.name/with-many-parts-1234567890", false},

		// \x 十六进制转义：连续多个才算，单个合法转义必须放行。
		{"single hex escape", `printf '\x41'`, false},
		{"two hex escapes", `\x72\x6d`, false},
		{"three hex escapes", `\x72\x6d\x20`, true},
		{"hex escape run of command", `\x72\x6d\x20\x2d\x72\x66\x20\x2f`, true},
		{"uppercase hex escapes", `\X72\X6D\X20\X2D\X72\X66`, true},
		{"malformed hex escape breaks run", `\x72\x6d\xzz\x20\x2d`, false},
		{"escaped CJK bytes are an encoded form", `\xe4\xb8\xad\xe6\x96\x87`, true},

		// Unicode 转义：\uHHHH 与 \UHHHHHHHH，连续多个才算。
		{"single unicode escape", `\u4e2d`, false},
		{"two unicode escapes", `\u0072\u006d`, true},
		{"unicode escape run of command", `\u0072\u006d\u0020\u002d\u0072\u0066`, true},
		{"uppercase U escapes", `\U00000072\U0000006d`, true},
		{"malformed unicode escape breaks run", `\u0072\u00zz\u002d`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksEncodedPayload(tc.s); got != tc.want {
				t.Fatalf("looksEncodedPayload(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

// TestFindEncodedPayloadEvidence 验证返回的证据是输入的原文子串，这样命令守卫
// 可以直接把它作为违规 token 报告。
func TestFindEncodedPayloadEvidence(t *testing.T) {
	cases := []string{
		"echo YmFzaCAtYyAncm0gLXJmIC8n | base64 -d",
		`printf '\x72\x6d\x20\x2d\x72\x66'`,
		`bash -c $'\u0072\u006d\u0020\u002d\u0072\u0066'`,
	}
	for _, s := range cases {
		evidence := findEncodedPayload(s)
		if evidence == "" {
			t.Fatalf("findEncodedPayload(%q) returned no evidence", s)
		}
		if !strings.Contains(s, evidence) {
			t.Fatalf("evidence %q is not a substring of %q", evidence, s)
		}
	}
}

// TestLooksEncodedPayloadIsStable 检查确定性与字节安全：任意字节输入都不 panic，
// 重复调用结果一致，且结果只取决于输入本身。
func TestLooksEncodedPayloadIsStable(t *testing.T) {
	inputs := []string{
		"",
		"git status",
		"echo 中文 \x00\xff",
		"\xff\xfe\x00\x80",
		`\x72\x6d`,
		"YmFzaCAtYyAncm0gLXJmIC8n",
	}
	for _, s := range inputs {
		first := looksEncodedPayload(s)
		for range 3 {
			if again := looksEncodedPayload(s); again != first {
				t.Fatalf("looksEncodedPayload(%q) is not deterministic", s)
			}
		}
	}
}

// TestLooksEncodedPayloadIsBounded 验证超长输入的处理有上界：超长 base64 串只取
// 固定前缀判定，证据长度不超过该上限；巨量非文本字节不会造成误报或过度分配。
func TestLooksEncodedPayloadIsBounded(t *testing.T) {
	// 5000 个字节 'a' 的 base64 串超过 maxBase64RunLen，但前缀仍解码为可打印文本。
	longText := strings.Repeat("a", 5000)
	encoded := base64.StdEncoding.EncodeToString([]byte(longText))
	evidence := findEncodedPayload(encoded)
	if evidence == "" {
		t.Fatal("long base64 payload of printable text was not detected")
	}
	if len(evidence) > maxBase64RunLen {
		t.Fatalf("evidence length %d exceeds the bounded scan limit %d", len(evidence), maxBase64RunLen)
	}

	// 巨量 base64 字母串解码为高位字节，必须放行；扫描保持线性且无状态。
	if looksEncodedPayload(strings.Repeat("x", 1<<17)) {
		t.Fatal("a long run of non-text base64 bytes must not be flagged")
	}
}

// TestCommandGuardDetectsEncodedPayload 验证编码注入已经并入命令守卫：无论显式
// 编码形态出现在原始命令还是 shell -c 载荷里，都会得到 HIGH 置信度的威胁匹配，
// 并被 FindFileEditingCommand 当作违规 token 返回。
func TestCommandGuardDetectsEncodedPayload(t *testing.T) {
	cases := []string{
		"echo YmFzaCAtYyAncm0gLXJmIC8n | base64 -d",
		`printf '\x72\x6d\x20\x2d\x72\x66\x20\x2f' | sh`,
		`bash -c $'\u0072\u006d\u0020\u002d\u0072\u0066'`,
	}
	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			match := DetectThreat(command)
			if match.Category != ThreatEncodedExec || !match.IsHigh() {
				t.Fatalf("DetectThreat(%q) = %+v, want HIGH encoded-exec", command, match)
			}
			token := FindFileEditingCommand(command)
			if token == "" {
				t.Fatalf("FindFileEditingCommand(%q) did not block the encoded payload", command)
			}
			if !strings.Contains(command, token) {
				t.Fatalf("token %q is not a substring of command %q", token, command)
			}
		})
	}
}
