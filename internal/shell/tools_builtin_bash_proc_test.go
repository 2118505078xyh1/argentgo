package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/proc"
)

// 本文件覆盖默认 bash 主路径的进程硬化 opt-in（ARGENT_BASH_USE_PROC）。
// 所有用例都不并行：开关通过 t.Setenv 隔离，proc.MaxOutputBytes 是全局量，
// 都有显式恢复，避免状态泄漏破坏"默认关、逐字节不变"的契约。
//
// 注：各用例显式设 ARGENT_BUILTIN_SHELL=0。内建 shell 已默认开启，0 是唯一的
// 紧急关闭值，用来强制走本文件要测的系统 shell / proc 路径（空值已不再关闭）。

// runBashToolWithTimeout 以给定的 timeout_seconds 调用 bash 工具。
func runBashToolWithTimeout(t *testing.T, command string, timeoutSeconds int) (string, error) {
	t.Helper()
	args := map[string]any{"command": command}
	if timeoutSeconds > 0 {
		args["timeout_seconds"] = timeoutSeconds
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return bashTool{}.Run(context.Background(), t.TempDir(), raw)
}

// TestBashUseProcFlagValues 锁定 opt-in 的解析语义：只有 TrimSpace 后
// 恰为 "1" 才开启，其余值（含 "true"/"yes"）都关闭，与 builtinShell 一致。
func TestBashUseProcFlagValues(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"1", true}, {" 1 ", true},
		{"", false}, {"0", false}, {"true", false}, {"yes", false}, {"01", false},
	} {
		t.Run(fmt.Sprintf("%q", tc.val), func(t *testing.T) {
			t.Setenv("ARGENT_BASH_USE_PROC", tc.val)
			if got := bashUseProcEnabled(); got != tc.want {
				t.Fatalf("val=%q enabled=%v want %v", tc.val, got, tc.want)
			}
		})
	}
}

// TestBashProcCommandHardening 验证硬化路径确实请求了进程组杀树与
// Windows 隐藏窗口，并把路径/参数/工作目录原样传给 proc。
func TestBashProcCommandHardening(t *testing.T) {
	argv := []string{"-lc", "echo hi"}
	c := bashProcCommand("bash", argv, `C:\work`)
	if c.Path != "bash" || c.Dir != `C:\work` {
		t.Fatalf("command path/dir = %q/%q", c.Path, c.Dir)
	}
	if len(c.Args) != 2 || c.Args[0] != "-lc" || c.Args[1] != "echo hi" {
		t.Fatalf("command args = %q", c.Args)
	}
	if !c.KillTree {
		t.Fatal("硬化路径必须设置 KillTree，否则无法整棵进程树终止")
	}
	if want := runtime.GOOS == "windows"; c.NoWindow != want {
		t.Fatalf("NoWindow = %v, want %v", c.NoWindow, want)
	}
}

// mixedStreamCommand 交替写 stdout/stderr，并用 sleep 强制两个流之间
// 有足够间隔，使 CombinedOutput 的交错顺序确定可断言。
const mixedStreamCommand = `printf 'O1\n'; sleep 0.15; printf 'E1\n' >&2; sleep 0.15; printf 'O2\n'`

// TestBashUseProcOffByteBaseline 验证开关关闭时输出与原有
// exec.Cmd.CombinedOutput 路径逐字节一致（此处为 O1、E1、O2 的交错顺序）。
func TestBashUseProcOffByteBaseline(t *testing.T) {
	requireBashShell(t)
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")
	t.Setenv("ARGENT_BASH_USE_PROC", "0")

	path, argv := shellInvocation(mixedStreamCommand)
	cmd := exec.Command(path, argv...)
	cmd.Dir = t.TempDir()
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	want := clipBashOutput(context.Background(), string(raw))
	if want != "O1\nE1\nO2\n" {
		t.Fatalf("测试前提失败：基线应为交错顺序，got %q", want)
	}

	got, err := runBashTool(t, mixedStreamCommand)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("开关关闭时输出与 CombinedOutput 基线不一致：got %q, want %q", got, want)
	}
}

// TestBashUseProcOnSegmentsStreams 固定 opt-in 下的已知差异：
// proc.Result.Combined() 是 stdout 段后接 stderr 段的拼接，而非实时交错。
func TestBashUseProcOnSegmentsStreams(t *testing.T) {
	requireBashShell(t)
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")
	t.Setenv("ARGENT_BASH_USE_PROC", "1")

	got, err := runBashTool(t, mixedStreamCommand)
	if err != nil {
		t.Fatal(err)
	}
	const want = "O1\nO2\nE1\n"
	if got != want {
		t.Fatalf("opt-in 输出应为 stdout 段 + stderr 段：got %q, want %q", got, want)
	}
}

// TestBashUseProcOnBoundsMassiveOutput 验证海量输出在有界捕获下不会
// 撑爆内存：把 proc 的每流上限临时降到 4KB，命令产生约 1.5MB，
// 结果必须保持很小且带有 proc 的截断标记。
func TestBashUseProcOnBoundsMassiveOutput(t *testing.T) {
	requireBashShell(t)
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")
	t.Setenv("ARGENT_BASH_USE_PROC", "1")

	old := proc.MaxOutputBytes
	proc.MaxOutputBytes = 4 << 10
	defer func() { proc.MaxOutputBytes = old }()

	command := `i=0; while [ $i -lt 15000 ]; do printf '%0100d\n' 0; i=$((i+1)); done`
	got, err := runBashTool(t, command)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 8192 {
		t.Fatalf("捕获输出未被限幅：%d 字节", len(got))
	}
	if !strings.Contains(got, "[output truncated]") {
		t.Fatalf("输出未带 proc 的截断标记：%q", got[:min(200, len(got))])
	}
	if !utf8.ValidString(got) {
		t.Fatal("限幅后的输出不是合法 UTF-8")
	}
}

// TestBashUseProcOnKeepsUTF8Intact 验证跨读取分块与截断边界都不切坏
// 多字节字符：约 75KB 的连续中文（超过默认 64KB 上限）不得产生 U+FFFD。
func TestBashUseProcOnKeepsUTF8Intact(t *testing.T) {
	requireBashShell(t)
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")
	t.Setenv("ARGENT_BASH_USE_PROC", "1")

	command := `i=0; while [ $i -lt 25000 ]; do printf '中'; i=$((i+1)); done`
	got, err := runBashTool(t, command)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) {
		t.Fatal("UTF-8 被切坏：输出不是合法 UTF-8")
	}
	if strings.Contains(got, "\ufffd") {
		t.Fatal("输出中出现 U+FFFD，UTF-8 在分块/截断边界被拆开")
	}
	if !strings.Contains(got, "中") {
		t.Fatalf("输出丢失中文内容：%q", got[:min(200, len(got))])
	}
}

// TestBashUseProcOnSanitizesHostEnv 验证 env 净化的可观察效果：
// 宿主私有变量 ARGENT_SESSION_ID 不应被硬化路径的子进程继承。
func TestBashUseProcOnSanitizesHostEnv(t *testing.T) {
	requireBashShell(t)
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")
	t.Setenv("ARGENT_BASH_USE_PROC", "1")
	t.Setenv("ARGENT_SESSION_ID", "leak-me")

	got, err := runBashTool(t, `printf '%s' "$ARGENT_SESSION_ID"`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "leak-me") {
		t.Fatalf("宿主私有环境变量泄漏到子进程：%q", got)
	}
}

// markerSize 返回标记文件当前大小；不存在时返回 0。
func markerSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return info.Size()
}

// TestBashUseProcOnTimeoutKillsDescendants 验证进程组杀树：
// 后台孙进程每 100ms 向标记文件追加一个字节，主 shell 长睡；
// 超时后若整棵进程树被终止，标记文件应停止增长。
func TestBashUseProcOnTimeoutKillsDescendants(t *testing.T) {
	requireBashShell(t)
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")
	t.Setenv("ARGENT_BASH_USE_PROC", "1")

	mark := filepath.Join(t.TempDir(), "alive")
	command := `(i=0; while [ $i -lt 100 ]; do printf x >> '` + filepath.ToSlash(mark) + `'; sleep 0.1; i=$((i+1)); done) & sleep 30`

	started := time.Now()
	_, err := runBashToolWithTimeout(t, command, 1)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("超时耗时 %s，孙进程可能仍持有 shell 未退出", elapsed)
	}

	before := markerSize(t, mark)
	time.Sleep(400 * time.Millisecond)
	after := markerSize(t, mark)
	if after != before {
		t.Fatalf("孙进程在进程树终止后仍在写文件：%d -> %d", before, after)
	}
}
