package bash

import (
	"os"
	"strings"
	"testing"
)

// controlOptions 为控制结构测试提供带 PATH 的环境（外部命令 test/expr 需要）。
func controlOptions(dir string) Options {
	vars := map[string]string{}
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i > 0 {
			vars[e[:i]] = e[i+1:]
		}
	}
	return Options{Cwd: dir, Vars: vars, Safety: func(string) bool { return true }}
}

// fields 把输出归一化为空白分隔的词序列，忽略换行差异。
func fields(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestControlStructures 覆盖 P1 原生实现的全部控制结构：条件、循环、分支、
// 函数、作用域、控制流信号。用例同时验证 Supported() 为 true。
func TestControlStructures(t *testing.T) {
	cases := []struct {
		name, cmd, want string
	}{
		{"if-then-else", `if true; then echo yes; else echo no; fi`, "yes"},
		{"if-elif", `if false; then echo A; elif true; then echo B; else echo C; fi`, "B"},
		{"if-no-branch", `if false; then echo A; fi`, ""},
		{"for-in", `for i in a b c; do echo $i; done`, "a b c"},
		{"for-positional", `f() { for x; do echo $x; done; }; f p q`, "p q"},
		{"while-break", `while true; do echo x; break; done`, "x"},
		{"while-false", `while false; do echo x; done`, ""},
		{"until", `i=0; until test $i = 1; do echo once; i=1; done`, "once"},
		{"case", `case b in a) echo A;; b) echo B;; *) echo X;; esac`, "B"},
		{"case-pattern", `case abc in a*) echo STAR;; esac`, "STAR"},
		{"case-multi-pattern", `case b in a|b) echo AB;; esac`, "AB"},
		{"func-basic", `f() { echo hi; }; f`, "hi"},
		{"func-args", `f() { echo $1 $2; }; f aa bb`, "aa bb"},
		{"func-argcount", `f() { echo $#; }; f a b c`, "3"},
		{"func-return", `f() { return 7; }; f; echo $?`, "7"},
		{"continue-break", `for i in 1 2 3 4; do if test $i = 2; then continue; fi; if test $i = 4; then break; fi; echo $i; done`, "1 3"},
		{"multiline", "if true\nthen\necho multi\nfi", "multi"},
		{"nested", `for i in 1 2; do for j in a b; do echo $i$j; done; done`, "1a 1b 2a 2b"},
		{"local", `f() { local x=inside; echo $x; }; x=outside; f; echo $x`, "inside outside"},
		{"block", `{ echo block; }`, "block"},
		{"func-keyword", `function g { echo kw; }; g`, "kw"},
		{"func-priority", `pwd() { echo custom-pwd; }; pwd`, "custom-pwd"},
	}

	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !Supported(tc.cmd) {
				t.Fatalf("Supported(%q) = false, want true", tc.cmd)
			}
			res, err := Run(tc.cmd, controlOptions(dir))
			if err != nil {
				t.Fatalf("Run(%q) error = %v", tc.cmd, err)
			}
			if got := fields(res.Output); got != tc.want {
				t.Fatalf("Run(%q) = %q, want %q (raw=%q)", tc.cmd, got, tc.want, res.Output)
			}
			if res.ExitStatus != 0 {
				t.Fatalf("Run(%q) exit = %d, want 0", tc.cmd, res.ExitStatus)
			}
		})
	}
}
