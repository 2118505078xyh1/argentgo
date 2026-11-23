package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestParseToolProgressTaggedASCII 验证带显式标记的进度文本提取出 ASCII 阶段标签。
func TestParseToolProgressTaggedASCII(t *testing.T) {
	label, ok := parseToolProgress("tool_progress: compiling")
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if label != "compiling" {
		t.Fatalf("label = %q, want %q", label, "compiling")
	}
}

// TestParseToolProgressTaggedCJKFullWidthColon 验证全角冒号与 CJK 标签均可识别，
// 且输出仍是有效的 UTF-8。
func TestParseToolProgressTaggedCJKFullWidthColon(t *testing.T) {
	label, ok := parseToolProgress("tool_progress：正在下载依赖")
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if label != "正在下载依赖" {
		t.Fatalf("label = %q, want %q", label, "正在下载依赖")
	}
	if !utf8.ValidString(label) {
		t.Fatalf("label is not valid UTF-8: %q", label)
	}
}

// TestParseToolProgressCJKLabelNotSplit 验证超长 CJK 标签按 rune 裁剪，
// 不会从 UTF-8 字符中间切开，且保留前缀内容。
func TestParseToolProgressCJKLabelNotSplit(t *testing.T) {
	long := strings.Repeat("阶段", maxToolProgressLabelRunes+10)
	label, ok := parseToolProgress("tool_progress: " + long)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if !utf8.ValidString(label) {
		t.Fatalf("label is not valid UTF-8")
	}
	if got := utf8.RuneCountInString(label); got != maxToolProgressLabelRunes {
		t.Fatalf("label runes = %d, want %d", got, maxToolProgressLabelRunes)
	}
	if !strings.HasPrefix(label, "阶段") {
		t.Fatalf("label = %q, want prefix 阶段", label)
	}
}

// TestParseToolProgressBareStage 验证 tools 包发出的裸阶段行取冒号前的阶段名，
// 且带 CJK 明细时仍返回纯 ASCII 标签。
func TestParseToolProgressBareStage(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"ascii detail", "bash job finished: go test ./...", "bash job finished"},
		{"cjk detail", "bash job finished: 编译完成 可以提交", "bash job finished"},
		{"no detail", "bash job finished", "bash job finished"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, ok := parseToolProgress(tc.text)
			if !ok {
				t.Fatalf("ok = false, want true")
			}
			if label != tc.want {
				t.Fatalf("label = %q, want %q", label, tc.want)
			}
		})
	}
}

// TestParseToolProgressCollapsesWhitespace 验证标签中的多余空白被折叠为单行。
func TestParseToolProgressCollapsesWhitespace(t *testing.T) {
	label, ok := parseToolProgress("tool_progress:   洗 涤\t\n  完成  ")
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if label != "洗 涤 完成" {
		t.Fatalf("label = %q, want %q", label, "洗 涤 完成")
	}
}

// TestParseToolProgressCaseInsensitive 验证进度标记大小写不敏感。
func TestParseToolProgressCaseInsensitive(t *testing.T) {
	label, ok := parseToolProgress("TOOL_PROGRESS: indexing")
	if !ok || label != "indexing" {
		t.Fatalf("got (%q, %v), want (%q, true)", label, ok, "indexing")
	}
}

// TestParseToolProgressRejectsNonProgress 验证空串、纯空白、普通文本和形近词
// 都返回 ok=false，确保桥接器不会误把其它通知当作进度。
func TestParseToolProgressRejectsNonProgress(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"whitespace", "   \t\n "},
		{"plain text", "hello world"},
		{"streaming text", "during execution"},
		{"lookalike", "tool_progressed"},
		{"other notice", "error: something went wrong"},
		{"marker without label", "tool_progress:"},
		{"marker without separator", "tool_progressXYZ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, ok := parseToolProgress(tc.text)
			if ok {
				t.Fatalf("ok = true for %q, want false (label=%q)", tc.text, label)
			}
			if label != "" {
				t.Fatalf("label = %q, want empty", label)
			}
		})
	}
}

// TestParseToolProgressExportedWrapper 验证导出入口与内部实现行为一致。
func TestParseToolProgressExportedWrapper(t *testing.T) {
	gotLabel, gotOK := ParseToolProgress("tool_progress: 打包")
	wantLabel, wantOK := parseToolProgress("tool_progress: 打包")
	if gotLabel != wantLabel || gotOK != wantOK {
		t.Fatalf("wrapper = (%q, %v), direct = (%q, %v)", gotLabel, gotOK, wantLabel, wantOK)
	}
}

// TestHandleEventToolProgress 验证进度事件经既有通知路径显示可解析的标签，
// 而不可解析的负载不产生任何记录。
func TestHandleEventToolProgress(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{Kind: EventToolProgress, Detail: "tool_progress: 正在构建"})
	if len(m.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(m.entries))
	}
	if m.entries[0].Kind != EntryNotice || m.entries[0].Text != "正在构建" {
		t.Fatalf("entry = %+v, want notice with text 正在构建", m.entries[0])
	}

	m.HandleEvent(Event{Kind: EventToolProgress, Detail: "unrelated notice"})
	if len(m.entries) != 1 {
		t.Fatalf("entries = %d after non-progress, want 1", len(m.entries))
	}
}

// TestParseToolProgressRunningBashIsIdempotent 验证 "bash: running <摘要>" 阶段标签
// 在 app 桥接器与 TUI 的两次解析中保持完整（含命令摘要），不会退化为纯工具名。
func TestParseToolProgressRunningBashIsIdempotent(t *testing.T) {
	for _, text := range []string{
		"bash: running go test ./... -count=1",
		"bash: running 编译并完成打包",
		"bash: running session",
	} {
		t.Run(text, func(t *testing.T) {
			first, ok := parseToolProgress(text)
			if !ok {
				t.Fatalf("first parse of %q failed", text)
			}
			second, ok := parseToolProgress(first)
			if !ok {
				t.Fatalf("second parse of %q failed", first)
			}
			if first != second {
				t.Fatalf("parse not idempotent: first = %q, second = %q", first, second)
			}
			if !strings.HasPrefix(second, "bash: running ") {
				t.Fatalf("label = %q, want prefix bash: running", second)
			}
		})
	}
}
