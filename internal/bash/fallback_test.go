package bash

import (
	"errors"
	"fmt"
	"testing"
)

// TestSupportedRecursive 验证 Supported 递归覆盖整棵 AST：命令替换体、
// 双引号内嵌套替换、重定向目标、二元命令两侧任一处有不支持构造即整条返回 false。
func TestSupportedRecursive(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		// —— 应原生支持（不得误伤）——
		{"普通命令", `echo hello`, true},
		{"简单命令替换", `echo $(echo hi)`, true},
		{"替换体内的普通管道", `echo $(echo hi | cat)`, true},
		{"替换体参数恰为保留字样文本", `echo $(echo for)`, true},
		{"多层嵌套的正常替换", `echo $(echo $(echo hi))`, true},
		{"双引号内正常替换", `echo "你好 $(echo 世界)"`, true},
		{"CJK 普通命令", `echo 中文参数`, true},
		{"重定向目标为普通替换", `echo hi > $(echo out.txt)`, true},
		{"普通重定向", `echo hi > out.txt`, true},

		// —— P1 起控制结构原生支持（包括嵌套在替换体里）——
		{"替换体内 for 循环", `echo $(for i in a; do echo $i; done)`, true},
		{"替换体内 while 循环", `echo $(while true; do echo x; done)`, true},
		{"替换体内 if", `echo $(if true; then echo x; fi)`, true},
		{"替换体内函数", `echo $(function f { echo x; })`, true},
		{"嵌套替换里含 for", `echo $(echo $(for i in a; do echo $i; done))`, true},
		{"替换体内管道接 for", `echo $(echo hi | for i in a; do echo $i; done)`, true},
		{"替换体内 && 接 for", `echo $(true && for i in a; do echo $i; done)`, true},
		{"双引号内替换体含 for", `echo "$(for i in a; do echo $i; done)"`, true},
		{"CJK 包裹的替换体含 for", `echo 前$(for i in a; do echo $i; done)后`, true},

		// —— 命令替换体内含真正不支持的构造（整条回退）——
		{"替换体内后台任务", `echo $(echo hi &)`, false},
		{"替换体内子 shell", `echo $( (echo hi) )`, false},

		// —— 重定向目标含命令替换 ——
		{"重定向目标替换体含 for", `echo hi > $(for i in a; do echo $i; done)`, true},
		{"输入重定向目标替换体含 while", `cat < $(while true; do echo x; done)`, true},
		{"追加重定向目标替换体含子 shell", `echo hi >> $( (echo out) )`, false},

		// —— 顶层不支持构造与未闭合构造 ——
		{"顶层 for 循环", `for i in a; do echo $i; done`, true},
		{"顶层后台任务", `echo hi &`, false},
		{"顶层子 shell", `( echo hi )`, false},
		{"注释", `# 注释`, false},
		{"算术展开", `echo $((1+2))`, false},
		{"未闭合命令替换", `echo $(echo hi`, false},
		{"未闭合双引号", `echo "hi`, false},
		{"未闭合单引号", `echo 'hi`, false},
		{"未闭合参数展开", `echo ${FOO`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Supported(tc.cmd); got != tc.want {
				t.Fatalf("Supported(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestShouldFallback 验证运行时回退判定：识别哨兵、带构造名的错误及 %w 包装，
// 不误伤其它错误（包括语义相近但不同哨兵的 ErrUnsupportedCommand）。
func TestShouldFallback(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"哨兵", ErrUnsupported, true},
		{"带构造名", &UnsupportedError{Kind: "for"}, true},
		{"%w 包装哨兵", fmt.Errorf("执行替换: %w", ErrUnsupported), true},
		{"%w 包装带构造名", fmt.Errorf("执行替换: %w", &UnsupportedError{Kind: "while"}), true},
		{"其它哨兵", ErrCommandNotFound, false},
		{"普通错误", errors.New("boom"), false},
		{"语法错误", &ParseError{Pos: Pos{Line: 1, Col: 1}, Msg: "bad"}, false},
		{"词法错误", &LexError{Pos: Pos{Line: 1, Col: 1}, Msg: "bad"}, false},
		{"相近但不同的哨兵", ErrUnsupportedCommand, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldFallback(tc.err); got != tc.want {
				t.Fatalf("ShouldFallback(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRuntimeFallbackForNestedUnsupported 证明运行时安全网：即便静态预检被绕过，
// 嵌套命令替换体里的不支持构造最终仍以 ErrUnsupported 冒出，ShouldFallback 为真。
// 调用方据此用原始命令行整条回退。
func TestRuntimeFallbackForNestedUnsupported(t *testing.T) {
	cmd := `echo $(echo hi &)`
	if Supported(cmd) {
		t.Fatalf("%q: 静态预检应判为不支持", cmd)
	}
	_, err := Run(cmd, testOptions(t.TempDir()))
	if err == nil {
		t.Fatalf("%q: 运行时应返回不支持错误", cmd)
	}
	if !ShouldFallback(err) {
		t.Fatalf("%q: 运行时错误应触发整条回退, err=%v", cmd, err)
	}
}

// TestCompatibilityMatrix 守护兼容矩阵的显性声明：结构完整、两类判定都有代表。
func TestCompatibilityMatrix(t *testing.T) {
	m := CompatibilityMatrix()
	if len(m) == 0 {
		t.Fatal("兼容矩阵不能为空")
	}
	var hasNative, hasFallback bool
	for i, e := range m {
		if e.Syntax == "" || e.Note == "" {
			t.Errorf("矩阵第 %d 项缺少说明: %+v", i, e)
		}
		if e.Native {
			hasNative = true
		} else {
			hasFallback = true
		}
	}
	if !hasNative || !hasFallback {
		t.Fatalf("矩阵需同时声明原生支持与回退两类: native=%v fallback=%v", hasNative, hasFallback)
	}
	// 返回的应是副本，改动不影响包内声明。
	m[0].Native = !m[0].Native
	if m[0].Native == compatibilityMatrix[0].Native {
		t.Fatal("CompatibilityMatrix 应返回副本")
	}
}
