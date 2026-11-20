package cli

import (
	"strings"
	"testing"
)

// TestConfirmAdviceTTY 确认有终端时生成"将执行 X，输入 y 确认"的交互文案。
func TestConfirmAdviceTTY(t *testing.T) {
	const action = "删除会话 s1"
	got := confirmAdvice(action, true)
	want := "将执行 " + action + "，输入 y 确认"
	if got != want {
		t.Fatalf("confirmAdvice(%q, true) = %q, want %q", action, got, want)
	}
	if !strings.Contains(got, "输入 y 确认") {
		t.Fatalf("TTY advice missing confirmation hint: %q", got)
	}
}

// TestConfirmAdviceNonTTY 确认无终端时不展示 action，只提示改用 --yes 标志，
// 并且不会出现会让人误以为要读取输入的"输入 y"。
func TestConfirmAdviceNonTTY(t *testing.T) {
	for _, action := range []string{"删除会话 s1", "sudo rm -rf /", "", "   "} {
		got := confirmAdvice(action, false)
		if got != confirmNonTTYAdvice {
			t.Fatalf("confirmAdvice(%q, false) = %q, want %q", action, got, confirmNonTTYAdvice)
		}
		if !strings.Contains(got, "--yes") {
			t.Fatalf("non-TTY advice missing --yes flag: %q", got)
		}
		if strings.Contains(got, "输入 y") {
			t.Fatalf("non-TTY advice must not ask for input: %q", got)
		}
	}
}

// TestConfirmAdviceCJKActionIsIntact 确认 CJK action 被原样写入文案，不出现
// U+FFFD 替换字符（即没有在 UTF-8 字符中间被截断）。
func TestConfirmAdviceCJKActionIsIntact(t *testing.T) {
	const action = "重命名会话「火山方舟·华东」为 2026 年报"
	got := confirmAdvice(action, true)
	if !strings.Contains(got, action) {
		t.Fatalf("CJK action %q missing from advice: %q", action, got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("advice contains replacement rune (broken UTF-8): %q", got)
	}
}

// TestConfirmAdviceEmptyAction 确认空或纯空白（含 CJK 全角空格、控制字符）
// 的 action 在 TTY 分支退化为通用提示，不会留下"将执行 ，"这样的空参数。
func TestConfirmAdviceEmptyAction(t *testing.T) {
	cases := []string{"", "   ", "\t\n", "　\t", "\x1b[31m\x1b[0m"}
	for _, action := range cases {
		got := confirmAdvice(action, true)
		if !strings.Contains(got, "将执行操作") {
			t.Fatalf("action %q: empty advice should use generic wording: %q", action, got)
		}
		if strings.Contains(got, "将执行 ，") || strings.Contains(got, "将执行  ，") {
			t.Fatalf("action %q: empty parameter leaked into advice: %q", action, got)
		}
	}
}

// TestConfirmAdviceRedactsDangerousWords 确认危险词被脱敏：破坏性命令、提权词
// 和凭据词（不区分大小写）都不会原样出现在提示中，而是被占位符替换；同一
// 字符串里的普通 CJK 文本仍然保留。
func TestConfirmAdviceRedactsDangerousWords(t *testing.T) {
	cases := []struct {
		name   string
		action string
		forbid []string
		keep   string
	}{
		{
			name:   "destructive and privilege",
			action: "删除 /tmp 目录并 sudo rm -rf / 再 chmod 777 /etc",
			forbid: []string{"sudo", "rm -rf", "chmod 777"},
			keep:   "删除 /tmp 目录并",
		},
		{
			name:   "credentials case insensitive",
			action: "重置 PASSWORD 与 Token 并更新 api_key",
			forbid: []string{"PASSWORD", "Token", "api_key"},
			keep:   "重置",
		},
		{
			name:   "destructive SQL",
			action: "清理缓存（不会 drop table）",
			forbid: []string{"drop table"},
			keep:   "清理缓存（不会",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := confirmAdvice(tc.action, true)
			if !strings.Contains(got, confirmRedactedPlaceholder) {
				t.Fatalf("advice missing redaction placeholder: %q", got)
			}
			for _, word := range tc.forbid {
				if strings.Contains(strings.ToLower(got), strings.ToLower(word)) {
					t.Fatalf("dangerous word %q leaked into advice: %q", word, got)
				}
			}
			if !strings.Contains(got, tc.keep) {
				t.Fatalf("ordinary text %q missing from advice: %q", tc.keep, got)
			}
			if strings.ContainsRune(got, '\uFFFD') {
				t.Fatalf("advice contains replacement rune (broken UTF-8): %q", got)
			}
		})
	}
}

// TestConfirmAdviceStripsControlAndANSI 确认 action 中的换行与 ANSI 转义序列
// 被剥离，提示始终是一行纯文本，无法借转义序列操纵终端。
func TestConfirmAdviceStripsControlAndANSI(t *testing.T) {
	got := confirmAdvice("\x1b[31m删除\x1b[0m\n文件", true)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\n") {
		t.Fatalf("control/ANSI bytes leaked into advice: %q", got)
	}
	if !strings.Contains(got, "删除文件") {
		t.Fatalf("ordinary text missing after stripping controls: %q", got)
	}
}
