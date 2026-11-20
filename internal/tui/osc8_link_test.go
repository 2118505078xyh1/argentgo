package tui

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestMakeOSC8LinkGeneratesSTTerminatedSequence(t *testing.T) {
	for _, tc := range []struct {
		name, urlValue, text, want string
	}{
		{
			name:     "http link with label",
			urlValue: "https://example.com/docs",
			text:     "文档",
			want:     "\x1b]8;;https://example.com/docs\x1b\\文档\x1b]8;;\x1b\\",
		},
		{
			name:     "empty text falls back to URL",
			urlValue: "http://example.com",
			text:     "",
			want:     "\x1b]8;;http://example.com\x1b\\http://example.com\x1b]8;;\x1b\\",
		},
		{
			name:     "uppercase scheme is accepted",
			urlValue: "HTTPS://EXAMPLE.COM/a",
			text:     "link",
			want:     "\x1b]8;;HTTPS://EXAMPLE.COM/a\x1b\\link\x1b]8;;\x1b\\",
		},
		{
			name:     "CJK and emoji label",
			urlValue: "https://example.com/路径",
			text:     "中文😀 链接",
			want:     "\x1b]8;;https://example.com/路径\x1b\\中文😀 链接\x1b]8;;\x1b\\",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := makeOSC8Link(tc.urlValue, tc.text)
			if got != tc.want {
				t.Fatalf("makeOSC8Link(%q, %q) = %q, want %q", tc.urlValue, tc.text, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("output is not valid UTF-8: %q", got)
			}
			if !strings.HasPrefix(got, osc8Prefix) || !strings.HasSuffix(got, osc8Reset) {
				t.Fatalf("missing OSC 8 open/reset wrapper: %q", got)
			}
			if strings.Count(got, oscST) != 2 {
				t.Fatalf("want exactly two ST terminators, got %d in %q", strings.Count(got, oscST), got)
			}
		})
	}
}

func TestMakeOSC8LinkRejectsUnsafeURL(t *testing.T) {
	// 非法 URL 一律不生成 OSC 序列，只返回去掉控制字符的纯文本。
	for _, raw := range []string{
		"",
		"example.com",
		"//example.com",
		"/relative/path",
		"javascript:alert(1)",
		"data:text/html;base64,xx",
		"file:///etc/passwd",
		"ftp://example.com/x",
		"http://",
		"https:///path",
		"http://exa mple.com",
		"https://example.com/\x1b[2J",
	} {
		t.Run(raw, func(t *testing.T) {
			got := makeOSC8Link(raw, "点我")
			if got != "点我" {
				t.Fatalf("makeOSC8Link(%q, \"点我\") = %q, want plain text", raw, got)
			}
			if strings.ContainsRune(got, 0x1b) {
				t.Fatalf("unsafe URL leaked an escape byte: %q", got)
			}
		})
	}
}

func TestMakeOSC8LinkScrubsControlCharacters(t *testing.T) {
	urlValue := "https://example.com/x"
	text := "a\x1b]8;;javascript:alert(1)\x07b\nc\r\td\x00e"
	got := makeOSC8Link(urlValue, text)
	want := "\x1b]8;;https://example.com/x\x1b\\a]8;;javascript:alert(1)bcde\x1b]8;;\x1b\\"
	if got != want {
		t.Fatalf("makeOSC8Link() = %q, want %q", got, want)
	}
	// 包装用的 ESC/ST 是必需的，只检查 URI 与显示文本这两段载荷。
	rest := strings.TrimPrefix(got, osc8Prefix)
	uriEnd := strings.Index(rest, oscST)
	if uriEnd < 0 {
		t.Fatalf("no ST after URI in %q", got)
	}
	label := strings.TrimSuffix(rest[uriEnd+len(oscST):], osc8Reset)
	for _, payload := range []string{rest[:uriEnd], label} {
		for _, r := range payload {
			if unicode.IsControl(r) {
				t.Fatalf("control char %U survived in %q", r, payload)
			}
		}
	}
	if !preserveOSC8(got) {
		t.Fatalf("scrubbed link is not a detectable OSC 8 link: %q", got)
	}

	// URL 里的控制字符会提前终结 OSC，因此整条链接都被拒绝。
	if got := makeOSC8Link("https://example.com/\x07evil", "链接"); got != "链接" {
		t.Fatalf("control char in URL should degrade to plain text, got %q", got)
	}

	// 文本全是控制字符时退回 URL 作为标签。
	if got := makeOSC8Link("https://example.com", "\x1b\x07\n"); got !=
		"\x1b]8;;https://example.com\x1b\\https://example.com\x1b]8;;\x1b\\" {
		t.Fatalf("all-control text should fall back to URL, got %q", got)
	}
}

func TestMakeOSC8LinkRoundTripsThroughSanitizer(t *testing.T) {
	link := makeOSC8Link("https://example.com/文档", "查看文档 😀")
	if !preserveOSC8(link) {
		t.Fatalf("generated link is not detected: %q", link)
	}
	if got := stripCursorControl(link); got != link {
		t.Fatalf("stripCursorControl changed the link: got %q, want %q", got, link)
	}
	if got := normalizeToolOutput(link); got != link {
		t.Fatalf("normalizeToolOutput changed the link: got %q, want %q", got, link)
	}
	if !preserveOSC8(normalizeToolOutput(link)) {
		t.Fatalf("link was lost after normalization: %q", link)
	}

	// 从生成的序列里把 URI 与显示文本还原出来，验证结构完整。
	rest := strings.TrimPrefix(link, osc8Prefix)
	uriEnd := strings.Index(rest, oscST)
	if uriEnd < 0 {
		t.Fatalf("no ST after URI in %q", link)
	}
	uri := rest[:uriEnd]
	body := rest[uriEnd+len(oscST):]
	label := strings.TrimSuffix(body, osc8Reset)
	if uri != "https://example.com/文档" || label != "查看文档 😀" {
		t.Fatalf("round trip mismatch: uri=%q label=%q", uri, label)
	}
}

func TestPreserveOSC8DetectsOnlyCompleteLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"ST terminated link", "\x1b]8;;https://x\x1b\\text\x1b]8;;\x1b\\", true},
		{"BEL terminated link", "\x1b]8;;https://x\x07text\x1b]8;;\x07", true},
		{"link with params", "\x1b]8;id=1;https://x\x1b\\text\x1b]8;;\x1b\\", true},
		{"link embedded in text", "before \x1b]8;;https://x\x1b\\x\x1b]8;;\x1b\\ after", true},
		{"reset only", "\x1b]8;;\x1b\\", false},
		{"unterminated link", "\x1b]8;;https://x", false},
		{"other OSC payload", "\x1b]0;window title\x07", false},
		{"plain URL", "https://example.com", false},
		{"CSI colors only", "\x1b[31mred\x1b[0m", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := preserveOSC8(tc.in); got != tc.want {
				t.Fatalf("preserveOSC8(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
