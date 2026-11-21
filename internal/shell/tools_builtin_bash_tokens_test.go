package shell

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

const bashHintPrefix = "[output: ~"

// TestClipBashOutputAddsHintWhenClipped 验证过大的命令
// 输出会在头部获得简短的体量提示，且不干扰被裁剪的
// 正文或截断标记。
func TestClipBashOutputAddsHintWhenClipped(t *testing.T) {
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 512})
	raw := strings.Repeat("abcdefgh", 512) // 4096 bytes, well over the 512-byte cap
	got := clipBashOutput(ctx, raw)

	if !strings.HasPrefix(got, bashHintPrefix) {
		t.Fatalf("clipped output has no volume hint at the head: %q", got[:min(80, len(got))])
	}
	firstLine, _, _ := strings.Cut(got, "\n")
	if !strings.Contains(firstLine, "k tokens, clipped]") {
		t.Fatalf("hint has an unexpected format: %q", firstLine)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("output was not actually clipped: %q", got[:min(80, len(got))])
	}
}

// TestClipBashOutputUsesDefaultLimitHint 覆盖 context 中没有限制的分支：
// 超过 maxToolOutput 的输出仍必须被提示。
func TestClipBashOutputUsesDefaultLimitHint(t *testing.T) {
	raw := strings.Repeat("x", maxToolOutput+100)
	got := clipBashOutput(context.Background(), raw)
	if !strings.HasPrefix(got, bashHintPrefix) {
		t.Fatalf("output over the default limit has no hint: %q", got[:min(80, len(got))])
	}
}

// TestClipBashOutputNoHintWhenSmall 验证小输出被
// 逐字节原样返回，且没有体量提示。
func TestClipBashOutputNoHintWhenSmall(t *testing.T) {
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 512})
	const raw = "short output\n"
	got := clipBashOutput(ctx, raw)
	if got != raw {
		t.Fatalf("small output changed: got %q, want %q", got, raw)
	}
	if strings.Contains(got, bashHintPrefix) {
		t.Fatalf("small output got an unexpected hint: %q", got)
	}
}

// TestBashOutputTokensCoversWideAndNarrowRunes 验证这个轻量估算：
// CJK rune 每个算一个 token，其他 rune 按 chars/4（向上取整）。
func TestBashOutputTokensCoversWideAndNarrowRunes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii multiple of four", strings.Repeat("a", 400), 100},
		{"ascii rounded up", strings.Repeat("a", 5), 2},
		{"han", strings.Repeat("中", 100), 100},
		{"kana and hangul", "アイウ한글", 5},
		{"mixed", "中文测试", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bashOutputTokens(tc.text); got != tc.want {
				t.Fatalf("bashOutputTokens(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// TestClipBashOutputHintUsesCJKEstimate 验证提示反映的是 CJK 感知的
// 估算，而非朴素的 byte/4 或 rune/4 计数。
func TestClipBashOutputHintUsesCJKEstimate(t *testing.T) {
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 256})
	raw := strings.Repeat("中", 2000) // 2000 wide runes => ~2.0k tokens
	got := clipBashOutput(ctx, raw)
	firstLine, _, _ := strings.Cut(got, "\n")
	const want = "[output: ~2.0k tokens, clipped]"
	if firstLine != want {
		t.Fatalf("hint = %q, want %q", firstLine, want)
	}
}

// TestBashClippedOutputPreservesExitStatus 验证纯文本增强
// 没有改变退出状态处理：非零退出仍产生错误，
// 且（被裁剪的）输出仍带有提示。
func TestBashClippedOutputPreservesExitStatus(t *testing.T) {
	path, _ := shellInvocation("")
	if path == "cmd" {
		t.Skip("portable status command requires a POSIX shell")
	}
	// 某些 Windows 安装会暴露一个 WindowsApps bash.exe 存根，它不是
	// 可用的 shell；工具无法通过它运行任何命令。
	if resolved, err := exec.LookPath(path); err == nil && strings.Contains(resolved, "WindowsApps") {
		t.Skip("bash resolves to the WindowsApps stub, not a real shell")
	}
	command := `i=0; while [ "$i" -lt 3000 ]; do printf '%040d\n' 0; i=$((i+1)); done; exit 7`
	rawArgs, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}

	out, err := (bashTool{}).Run(context.Background(), t.TempDir(), rawArgs)
	if err == nil {
		t.Fatal("non-zero exit must still produce an error")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Fatalf("error lost the exit status: %q", err.Error())
	}
	if !strings.HasPrefix(out, bashHintPrefix) {
		t.Fatalf("clipped output has no volume hint: %q", out[:min(80, len(out))])
	}
}
