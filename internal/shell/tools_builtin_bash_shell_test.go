package shell

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 内建 shell 的 opt-in 由 ARGENT_BUILTIN_SHELL 控制。测试用 t.Setenv 隔离，
// 确保不影响其它用例（默认关闭的字节契约）。

func runBashTool(t *testing.T, command string) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"command": command})
	return bashTool{}.Run(context.Background(), t.TempDir(), raw)
}

func TestBuiltinShellOptInEcho(t *testing.T) {
	t.Setenv("ARGENT_BUILTIN_SHELL", "1")
	got, err := runBashTool(t, "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\n" {
		t.Fatalf("builtin shell echo => %q, want %q", got, "hello\n")
	}
}

func TestBuiltinShellOptInVariables(t *testing.T) {
	t.Setenv("ARGENT_BUILTIN_SHELL", "1")
	t.Setenv("MY_TEST_VAR", "works")
	got, err := runBashTool(t, "echo $MY_TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "works\n" {
		t.Fatalf("env expansion => %q", got)
	}
}

func TestBuiltinShellOptInSupportedSubset(t *testing.T) {
	t.Setenv("ARGENT_BUILTIN_SHELL", "1")
	// 管道 + 内建 coreutils。
	got, err := runBashTool(t, "echo apple | grep apple")
	if err != nil {
		t.Fatal(err)
	}
	if got != "apple\n" {
		t.Fatalf("pipe => %q", got)
	}
}

func TestBuiltinShellDefaultOffUsesSystemShell(t *testing.T) {
	// 不设置 ARGENT_BUILTIN_SHELL（确保为默认关闭）。
	os.Unsetenv("ARGENT_BUILTIN_SHELL")
	got, err := runBashTool(t, "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("default shell echo => %q", got)
	}
}

func TestBuiltinShellEnabledFlagValues(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"1", true}, {" 1 ", true},
		{"", false}, {"0", false}, {"true", false},
	} {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("ARGENT_BUILTIN_SHELL", tc.val)
			if got := builtinShellEnabled(); got != tc.want {
				t.Fatalf("val=%q enabled=%v want %v", tc.val, got, tc.want)
			}
		})
	}
}
