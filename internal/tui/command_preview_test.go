package tui

import (
	"strings"
	"testing"
)

// TestPreviewCommandTruncation 表驱动锁定截断边界：折叠空白、恰好等于上限
// 不截断、超限时以省略号收尾且总宽不超过上限、maxCells <= 0 回退默认值。
func TestPreviewCommandTruncation(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		maxCells int
		want     string
	}{
		{"empty", "", 10, ""},
		{"short passed through", "echo hi", 10, "echo hi"},
		{"whitespace collapsed", "echo\n\t  hi", 10, "echo hi"},
		{"exact fit", "abcdefgh", 8, "abcdefgh"},
		{"one over truncates", "abcdefghi", 8, "abcdefg…"},
		{"single cell budget", "abcdef", 1, "…"},
		{"default on zero", strings.Repeat("a", 70), 0, strings.Repeat("a", defaultPreviewCells-1) + "…"},
		{"default on negative", strings.Repeat("a", 70), -5, strings.Repeat("a", defaultPreviewCells-1) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreviewCommand(tt.command, tt.maxCells)
			if got != tt.want {
				t.Fatalf("PreviewCommand(%q, %d) = %q, want %q", tt.command, tt.maxCells, got, tt.want)
			}
			budget := tt.maxCells
			if budget <= 0 {
				budget = defaultPreviewCells
			}
			if w := TextWidth(got); w > budget {
				t.Fatalf("PreviewCommand width = %d, want <= %d (%q)", w, budget, got)
			}
			assertApprovalLineValid(t, got)
		})
	}
}

// TestPreviewCommandCJKEmojiSafe 验证 CJK、emoji、ZWJ 组合与旗帜按字素簇
// 截断：任何多字节字符都不会被切开，结果是合法 UTF-8 单行文本。
func TestPreviewCommandCJKEmojiSafe(t *testing.T) {
	const family = "👨‍👩‍👧" // ZWJ 家庭 emoji，按两个单元格计
	const flag = "🇨🇳"      // 区域指示符旗帜，按两个单元格计

	tests := []struct {
		name     string
		command  string
		maxCells int
		want     string
	}{
		{
			name:     "CJK truncates on whole rune",
			command:  strings.Repeat("字", 20),
			maxCells: 11,
			want:     strings.Repeat("字", 5) + "…",
		},
		{
			name:     "emoji truncates on whole rune",
			command:  strings.Repeat("😀", 20),
			maxCells: 11,
			want:     strings.Repeat("😀", 5) + "…",
		},
		{
			name:     "ZWJ family stays intact",
			command:  strings.Repeat(family, 10),
			maxCells: 11,
			want:     strings.Repeat(family, 5) + "…",
		},
		{
			name:     "flag stays intact",
			command:  strings.Repeat(flag, 10),
			maxCells: 5,
			want:     strings.Repeat(flag, 2) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreviewCommand(tt.command, tt.maxCells)
			if got != tt.want {
				t.Fatalf("PreviewCommand = %q, want %q", got, tt.want)
			}
			if w := TextWidth(got); w > tt.maxCells {
				t.Fatalf("width = %d, want <= %d (%q)", w, tt.maxCells, got)
			}
			assertApprovalLineValid(t, got)
		})
	}
}

// TestPreviewCommandEscapesControl 验证控制字符被转义成可见的 "\xNN"：
// ESC/NUL/BEL/DEL 都会现形；换行/制表/回车则先被折叠成空格，绝不留下
// 实际换行；转义后的序列同样参与单元格预算。
func TestPreviewCommandEscapesControl(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		maxCells int
		want     string
	}{
		{"ESC escaped", "echo\x1b[31mred", 60, `echo\x1b[31mred`},
		{"NUL and DEL escaped", "a\x00b\x7fc", 60, `a\x00b\x7fc`},
		{"BEL escaped", "echo\x07!", 60, `echo\x07!`},
		{"newline tab CR collapsed", "echo\n\ta\r b", 60, "echo a b"},
		{"mixed collapse and escape", "echo\x07\n hi", 60, `echo\x07 hi`},
		{"escaped controls counted in budget", strings.Repeat("\x1b", 30), 10, `\x1b\x1b\` + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreviewCommand(tt.command, tt.maxCells)
			if got != tt.want {
				t.Fatalf("PreviewCommand(%q, %d) = %q, want %q", tt.command, tt.maxCells, got, tt.want)
			}
			if w := TextWidth(got); w > tt.maxCells {
				t.Fatalf("width = %d, want <= %d (%q)", w, tt.maxCells, got)
			}
			assertApprovalLineValid(t, got)
		})
	}
}

// TestCommandGlyphs 表驱动锁定危险符号提取：无符号得空串、单符号、组合
// 重定向、命令替换、最长匹配优先、重复出现保留、以及 CJK 命令照常扫描。
func TestCommandGlyphs(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"empty", "", ""},
		{"no dangerous symbols", "echo hello", ""},
		{"single semicolon", "echo a; echo b", ";"},
		{"logical and", "a && b", "&&"},
		{"logical or", "a || b", "||"},
		{"pipe", "cat a | grep b", "|"},
		{"redirect", "echo x > f", ">"},
		{"append redirect", "echo x >> f", ">>"},
		{"combined redirect", "cmd 2>&1", "2>&1"},
		{"command substitution", "echo $(date)", "$("},
		{"all symbols in order", "a; b && c || d | e > f >> g 2>&1 $(h)", "; && || | > >> 2>&1 $("},
		{"longest match wins", "a ||| b", "|| |"},
		{"duplicates preserved", "a; b; c", "; ;"},
		{"single ampersand ignored", "a & b", ""},
		{"CJK command with glyphs", "删除; 文件 > out", "; >"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommandGlyphs(tt.command); got != tt.want {
				t.Fatalf("CommandGlyphs(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

// TestFormatCommandLine 表驱动锁定组合格式：光标 + 危险标记 + 预览 + 原因，
// 并确认命令部分确实复用了 PreviewCommand（含折叠与默认截断）。
func TestFormatCommandLine(t *testing.T) {
	tests := []struct {
		name    string
		command string
		reason  string
		danger  bool
		want    string
	}{
		{"plain", "echo hi", "", false, "$ echo hi"},
		{"danger adds mark", "rm -rf /", "", true, "$ ⚠ rm -rf /"},
		{"reason appended", "npm install", "网络请求", false, "$ npm install — 网络请求"},
		{"danger with reason", "sudo rm -rf /", "危险操作", true, "$ ⚠ sudo rm -rf / — 危险操作"},
		{"newlines collapsed", "echo\n  hi", "第一行\n第二行", false, "$ echo hi — 第一行 第二行"},
		{"command uses default truncation", strings.Repeat("x", 70), "", false,
			"$ " + strings.Repeat("x", defaultPreviewCells-1) + "…"},
		{"empty command", "", "", false, "$ "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCommandLine(tt.command, tt.reason, tt.danger)
			if got != tt.want {
				t.Fatalf("FormatCommandLine(%q, %q, %v) = %q, want %q",
					tt.command, tt.reason, tt.danger, got, tt.want)
			}
			// 复用性断言：命令部分的渲染与单独调用 PreviewCommand 一致。
			preview := PreviewCommand(tt.command, defaultPreviewCells)
			if !strings.Contains(got, preview) {
				t.Fatalf("FormatCommandLine = %q does not embed PreviewCommand = %q", got, preview)
			}
			assertApprovalLineValid(t, got)
		})
	}
}

// TestCommandPrimitivesPure 守护三个原语的纯函数契约：重复调用结果一致，
// 且不修改传入的 command / reason。Go 字符串不可变，此处显式锁定意图。
func TestCommandPrimitivesPure(t *testing.T) {
	command := "echo\x1b\n hi; rm > f"
	reason := "原因\n第二行"
	commandCopy, reasonCopy := command, reason

	if a, b := PreviewCommand(command, 20), PreviewCommand(command, 20); a != b {
		t.Fatalf("PreviewCommand not deterministic: %q vs %q", a, b)
	}
	if a, b := CommandGlyphs(command), CommandGlyphs(command); a != b {
		t.Fatalf("CommandGlyphs not deterministic: %q vs %q", a, b)
	}
	if a, b := FormatCommandLine(command, reason, true), FormatCommandLine(command, reason, true); a != b {
		t.Fatalf("FormatCommandLine not deterministic: %q vs %q", a, b)
	}
	if command != commandCopy || reason != reasonCopy {
		t.Fatal("inputs were mutated")
	}
}
