package shell

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// 内建 shell 现在默认启用：ARGENT_BUILTIN_SHELL=0 才是紧急关闭。测试用 t.Setenv
// 隔离，确保默认路径与显式关闭路径都各自成立、不互相泄漏。

func runBashTool(t *testing.T, command string) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"command": command})
	return bashTool{}.Run(context.Background(), t.TempDir(), raw)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestBuiltinShellEcho(t *testing.T) {
	t.Setenv("ARGENT_BUILTIN_SHELL", "") // 空值=未设置=默认启用
	got, err := runBashTool(t, "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\n" {
		t.Fatalf("builtin shell echo => %q, want %q", got, "hello\n")
	}
}

func TestBuiltinShellVariables(t *testing.T) {
	t.Setenv("ARGENT_BUILTIN_SHELL", "")
	t.Setenv("MY_TEST_VAR", "works")
	got, err := runBashTool(t, "echo $MY_TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "works\n" {
		t.Fatalf("env expansion => %q", got)
	}
}

func TestBuiltinShellSupportedSubset(t *testing.T) {
	t.Setenv("ARGENT_BUILTIN_SHELL", "")
	// 管道 + 内建 coreutils。
	got, err := runBashTool(t, "echo apple | grep apple")
	if err != nil {
		t.Fatal(err)
	}
	if got != "apple\n" {
		t.Fatalf("pipe => %q", got)
	}
}

func TestBuiltinShellDefaultOnUsesBuiltin(t *testing.T) {
	// 不设置 ARGENT_BUILTIN_SHELL（真正的未设置态），应默认走内建解释器。
	os.Unsetenv("ARGENT_BUILTIN_SHELL")
	if !builtinShellEnabled() {
		t.Fatal("未设置 ARGENT_BUILTIN_SHELL 时内建 shell 应默认启用")
	}
	got, err := runBashTool(t, "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\n" {
		t.Fatalf("默认内建 shell echo => %q, want %q", got, "hello\n")
	}
}

// TestBuiltinShellRuntimeFallbackToSystemShell 验证运行期安全网：命令通过静态
// 预检，但解释器在展开阶段发现程序名是保留字而返回 ErrUnsupported。此时
// ShouldFallback 必须为真，bash 工具用原始命令行整条回退系统 shell——结果与
// 显式 ARGENT_BUILTIN_SHELL=0 的系统 shell 路径逐字节一致，没有部分执行残留。
func TestBuiltinShellRuntimeFallbackToSystemShell(t *testing.T) {
	const cmd = `$(echo for)` // 展开后的程序名是保留字，静态预检看不出来
	if !Supported(cmd) {
		t.Fatalf("%q 应通过静态预检，才能覆盖运行期回退", cmd)
	}
	if _, err := Run(cmd, testOptions(t.TempDir())); !ShouldFallback(err) {
		t.Fatalf("%q 运行期应返回可回退错误, err=%v", cmd, err)
	}

	t.Setenv("ARGENT_BUILTIN_SHELL", "0")
	sysOut, sysErr := runBashTool(t, cmd)
	t.Setenv("ARGENT_BUILTIN_SHELL", "")
	gotOut, gotErr := runBashTool(t, cmd)
	if gotOut != sysOut || errText(gotErr) != errText(sysErr) {
		t.Fatalf("整条回退结果应与系统 shell 一致: got (%q, %v), want (%q, %v)",
			gotOut, gotErr, sysOut, sysErr)
	}
}

func TestBuiltinShellEnabledFlagValues(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"1", true}, {" 1 ", true},
		{"true", true}, {"", true}, // 未设置/空串/其它值=默认开
		{"0", false}, {" 0 ", false}, // 仅 0（修剪后）为紧急关闭
	} {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("ARGENT_BUILTIN_SHELL", tc.val)
			if got := builtinShellEnabled(); got != tc.want {
				t.Fatalf("val=%q enabled=%v want %v", tc.val, got, tc.want)
			}
		})
	}
}
