package cli

import (
	"strings"
	"testing"
)

// TestHelpFlagSpellingsPrintUsageAndExitZero 覆盖 Go flag 包接受的每一种帮助开关写法。
// parseArgs 过去只在遇到精确的 "-h"/"--help" 记号时才提前返回，并基于同样狭窄的判断
// 重新计算 Options.Help，因此 flag 解析器其他合法的写法会被静默丢弃：命令落入空 prompt
// 路径，并以退出码 2 将用法打印到 stderr。
func TestHelpFlagSpellingsPrintUsageAndExitZero(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"short", []string{"-h"}},
		{"long", []string{"--help"}},
		{"single dash long", []string{"-help"}},
		{"double dash short", []string{"--h"}},
		{"short equals true", []string{"-h=true"}},
		{"long equals true", []string{"--help=true"}},
		{"short equals one", []string{"-h=1"}},
		{"after a valid flag", []string{"--model", "m1", "-help"}},
		{"wins over an invalid flag", []string{"-help", "--protocol", "grpc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ioState, out, errOut := newTestIO("", nil)
			if code := RunWith(App{IO: ioState}, tc.args); code != 0 {
				t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
			}
			if !strings.Contains(out.String(), "usage: argentgo [flags] <prompt>") {
				t.Fatalf("stdout = %q, want usage", out.String())
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", errOut.String())
			}
		})
	}
}

// TestHelpFalseDoesNotPrintHelp 确保放宽后的帮助检测仍然尊重显式的 false 值，
// 而不会把任何 "help" 记号都当作在 stdout 打印用法的请求。
func TestHelpFalseDoesNotPrintHelp(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"--help=false"}); code != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q)", code, out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errOut.String(), "usage: argentgo [flags] <prompt>") {
		t.Fatalf("stderr = %q, want usage", errOut.String())
	}
}
