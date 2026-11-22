package bash

import (
	"reflect"
	"testing"
)

// TestDetectShellFeaturesPipe 检查管道符：单个 | 标为 HasPipe，
// 连续的 || 只标为 HasOr，不产生 HasPipe。
func TestDetectShellFeaturesPipe(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"single pipe", "cat a | grep b", ShellFeature{HasPipe: true}},
		{"pipe without spaces", "a|b", ShellFeature{HasPipe: true}},
		{"double pipe is or", "a || b", ShellFeature{HasOr: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesAndOr 检查逻辑与/或，并确认单个 & 不产生任何特性。
func TestDetectShellFeaturesAndOr(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"and", "a && b", ShellFeature{HasAnd: true}},
		{"or", "a || b", ShellFeature{HasOr: true}},
		{"single ampersand ignored", "a & b", ShellFeature{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesRedirect 检查各类重定向，包括 >> 与 2>&1。
func TestDetectShellFeaturesRedirect(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"greater", "echo a > b", ShellFeature{HasRedirect: true}},
		{"double greater", "echo a >> b", ShellFeature{HasRedirect: true}},
		{"less", "cat < b", ShellFeature{HasRedirect: true}},
		{"stderr to stdout", "cmd 2>&1", ShellFeature{HasRedirect: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesGlob 检查三种通配符 * ? [。
func TestDetectShellFeaturesGlob(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"star", "ls *.go", ShellFeature{HasGlob: true}},
		{"question", "ls a?.txt", ShellFeature{HasGlob: true}},
		{"bracket", "ls [ab].txt", ShellFeature{HasGlob: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesVarAndCmdSubst 检查变量展开与命令替换，
// 并确认 $* / $? 中的通配字节不会被重复计为通配符，孤立的 $ 不触发。
func TestDetectShellFeaturesVarAndCmdSubst(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"dollar var", "echo $HOME", ShellFeature{HasVar: true}},
		{"dollar question", "echo $?", ShellFeature{HasVar: true}},
		{"dollar brace", "echo ${HOME}", ShellFeature{HasVar: true}},
		{"dollar digit", "echo $1", ShellFeature{HasVar: true}},
		{"dollar star is var only", "echo $*", ShellFeature{HasVar: true}},
		{"dollar paren", "echo $(date)", ShellFeature{HasCmdSubst: true}},
		{"bare dollar", "echo cost $ left", ShellFeature{}},
		{"trailing dollar", "echo cost $", ShellFeature{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesSemicolon 检查分号。
func TestDetectShellFeaturesSemicolon(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"semicolon", "echo a; echo b", ShellFeature{HasSemicolon: true}},
		{"trailing semicolon", "echo a;", ShellFeature{HasSemicolon: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesQuotedNoTrigger 检查单引号与双引号内的元字符
// 都不触发；引号外的内容仍正常检测。双引号中的 \" 不应提前闭合引号。
func TestDetectShellFeaturesQuotedNoTrigger(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"single quoted pipe", "echo 'a | b'", ShellFeature{}},
		{"single quoted all", "echo 'a && b || c > d * $HOME $(x);'", ShellFeature{}},
		{"double quoted pipe", "echo \"a | b\"", ShellFeature{}},
		{"double quoted all", "echo \"a && b || c > d * $HOME $(x);\"", ShellFeature{}},
		{"escaped quote stays inside", "echo \"a \\\" | b\"", ShellFeature{}},
		{"mixed quoted", "echo 'a' \"b\" | cat", ShellFeature{HasPipe: true}},
		{"single quote after literal", "echo a 'b | c' > d", ShellFeature{HasRedirect: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesEscapedNoTrigger 检查反斜杠转义让元字符退化成
// 字面量，不被计入。
func TestDetectShellFeaturesEscapedNoTrigger(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"escaped pipe", "echo a \\| b", ShellFeature{}},
		{"escaped and", "echo a \\&\\& b", ShellFeature{}},
		{"escaped or", "echo a \\|\\| b", ShellFeature{}},
		{"escaped redirect", "echo a \\> b", ShellFeature{}},
		{"escaped glob", "echo \\*.go", ShellFeature{}},
		{"escaped dollar", "echo \\$HOME", ShellFeature{}},
		{"escaped semicolon", "echo a \\; b", ShellFeature{}},
		{"trailing backslash", "echo a \\", ShellFeature{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesCombinations 检查多个特性同时出现时的合并结果。
func TestDetectShellFeaturesCombinations(t *testing.T) {
	all := ShellFeature{
		HasPipe: true, HasAnd: true, HasOr: true, HasRedirect: true,
		HasGlob: true, HasVar: true, HasCmdSubst: true, HasSemicolon: true,
	}
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"pipeline with redirect", "cat a | grep b > out.txt", ShellFeature{HasPipe: true, HasRedirect: true}},
		{"all features", "a && b || c | d > e 2>&1; echo $X $(date) *.go", all},
		{"quoted then unquoted combo", "echo 'x | y'; cat \"$A\" || ls *.go", ShellFeature{
			HasSemicolon: true, HasOr: true, HasGlob: true,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesCJK 检查含 CJK 文本时的安全性：元字符仍被识别，
// 而 CJK 字节既不误触发，也不会因转义扫描被破坏。
func TestDetectShellFeaturesCJK(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    ShellFeature
	}{
		{"cjk around pipe", "回显 中文 | 过滤 你好", ShellFeature{HasPipe: true}},
		{"cjk no metachar", "回显 中文 你好", ShellFeature{}},
		{"cjk escaped pipe", "中文 \\| 过滤", ShellFeature{}},
		{"cjk quoted", "回显 '中文 | 管道'", ShellFeature{}},
		// shell 变量名只允许 ASCII 标识符，CJK 字符跟在 $ 后不构成变量展开。
		{"cjk after dollar not a var", "回显 $变量", ShellFeature{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != tc.want {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want %+v", tc.command, got, tc.want)
			}
		})
	}
}

// TestDetectShellFeaturesEmpty 检查空串与仅含空白/CJK 的命令不触发任何特性。
func TestDetectShellFeaturesEmpty(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{"empty", ""},
		{"only spaces", "   "},
		{"only cjk", "中文"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectShellFeatures(tc.command); got != (ShellFeature{}) {
				t.Fatalf("DetectShellFeatures(%q) = %+v, want zero value", tc.command, got)
			}
		})
	}
}

// TestDetectShellFeaturesDoesNotModifyInput 确认扫描不修改入参。
// Go 字符串不可变，这里显式记录该契约。
func TestDetectShellFeaturesDoesNotModifyInput(t *testing.T) {
	original := "echo 'a | b' | cat > out; echo $HOME $(date) *.go"
	input := original
	_ = DetectShellFeatures(input)
	if input != original {
		t.Fatalf("入参被修改: got %q, want %q", input, original)
	}
}

// TestNeedsShell 检查 NeedsShell 在任一特性为真时返回 true，
// 零值与全部为假时返回 false。
func TestNeedsShell(t *testing.T) {
	tests := []struct {
		name string
		f    ShellFeature
		want bool
	}{
		{"zero value", ShellFeature{}, false},
		{"pipe", ShellFeature{HasPipe: true}, true},
		{"and", ShellFeature{HasAnd: true}, true},
		{"or", ShellFeature{HasOr: true}, true},
		{"redirect", ShellFeature{HasRedirect: true}, true},
		{"glob", ShellFeature{HasGlob: true}, true},
		{"var", ShellFeature{HasVar: true}, true},
		{"cmd subst", ShellFeature{HasCmdSubst: true}, true},
		{"semicolon", ShellFeature{HasSemicolon: true}, true},
		{"all", ShellFeature{
			HasPipe: true, HasAnd: true, HasOr: true, HasRedirect: true,
			HasGlob: true, HasVar: true, HasCmdSubst: true, HasSemicolon: true,
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsShell(tc.f); got != tc.want {
				t.Fatalf("NeedsShell(%+v) = %t, want %t", tc.f, got, tc.want)
			}
		})
	}
}

// TestFeatureListStableOrder 检查 FeatureList 的中文名称与稳定顺序：
// 全部为真时顺序固定，子集保持相对顺序，空特性返回空列表。
func TestFeatureListStableOrder(t *testing.T) {
	all := ShellFeature{
		HasPipe: true, HasAnd: true, HasOr: true, HasRedirect: true,
		HasGlob: true, HasVar: true, HasCmdSubst: true, HasSemicolon: true,
	}
	wantAll := []string{"管道", "逻辑与", "逻辑或", "重定向", "通配符", "变量展开", "命令替换", "分号"}

	for run := 0; run < 3; run++ {
		if got := FeatureList(all); !reflect.DeepEqual(got, wantAll) {
			t.Fatalf("FeatureList(all) run %d = %v, want %v", run, got, wantAll)
		}
	}

	subset := ShellFeature{HasPipe: true, HasGlob: true, HasSemicolon: true}
	wantSubset := []string{"管道", "通配符", "分号"}
	if got := FeatureList(subset); !reflect.DeepEqual(got, wantSubset) {
		t.Fatalf("FeatureList(subset) = %v, want %v", got, wantSubset)
	}

	if got := FeatureList(ShellFeature{}); len(got) != 0 {
		t.Fatalf("FeatureList(zero) = %v, want empty", got)
	}
}
