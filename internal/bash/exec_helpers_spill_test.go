package bash

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// TestClipSpillCallbackPassthrough 验证注册表注入的溢出回调一旦报告已处理，
// clip 必须把其返回值逐字节透传，且只调用一次、传入原始文本。
func TestClipSpillCallbackPassthrough(t *testing.T) {
	const limit = 512
	text := strings.Repeat("spill-me", 512) // 4096 bytes, over the 512-byte cap
	const spilled = "\n[output-spill: 4096 bytes elided; full output: /tmp/artifact.log]\n"

	calls := 0
	spill := func(got string) (string, bool) {
		calls++
		if got != text {
			t.Fatalf("spill got %d bytes, want the original %d-byte text", len(got), len(text))
		}
		return spilled, true
	}
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{
		MaxOutput: limit,
		Spill:     spill,
	})

	if got := clip(ctx, text); got != spilled {
		t.Fatalf("clip = %q, want the spill result verbatim", got)
	}
	if calls != 1 {
		t.Fatalf("spill called %d times, want 1", calls)
	}
}

// TestClipWithoutSpillMatchesClipTo 锁定默认行为：未注入回调时，
// 各阈值下 clip 与 clipTo 的输出必须逐字节相同（含小输出和多字节 rune）。
func TestClipWithoutSpillMatchesClipTo(t *testing.T) {
	texts := []struct {
		name string
		text string
	}{
		{"small", "a short, ordinary output"},
		{"oversized", strings.Repeat("x", 4096)},
		{"multibyte", strings.Repeat("中文😀ab", 400)},
	}
	limits := []int{0, 256, 512}

	for _, tc := range texts {
		for _, limit := range limits {
			t.Run(tc.name+"/limit="+strconv.Itoa(limit), func(t *testing.T) {
				ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: limit})
				got := clip(ctx, tc.text)
				want := clipTo(tc.text, outputLimit(ctx))
				if got != want {
					t.Fatalf("clip without spill differs from clipTo: got %d bytes, want %d bytes", len(got), len(want))
				}
			})
		}
	}
}

// TestClipSpillCallbackNotHandledMatchesClipTo 验证回调报告未处理时，
// clip 回退到普通裁剪路径，输出与 clipTo 逐字节相同。
func TestClipSpillCallbackNotHandledMatchesClipTo(t *testing.T) {
	text := strings.Repeat("y", 4096)
	calls := 0
	spill := func(string) (string, bool) {
		calls++
		return "must be ignored", false
	}
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{
		MaxOutput: 512,
		Spill:     spill,
	})

	got := clip(ctx, text)
	want := clipTo(text, outputLimit(ctx))
	if got != want {
		t.Fatalf("clip with unhandled spill differs from clipTo: got %d bytes, want %d bytes", len(got), len(want))
	}
	if calls != 1 {
		t.Fatalf("spill called %d times, want 1", calls)
	}
}
