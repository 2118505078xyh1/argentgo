package bash

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// collapseMarkerRE 解析 [N earlier lines] 标记。
var collapseMarkerRE = regexp.MustCompile(`\[(\d+) earlier lines\]`)

// legacyClipBashOutput 复刻接线前的 clipBashOutput，作为 opt-in 关闭时的
// 逐字节基线。它与生产代码不共享新分支，能独立检出默认路径的意外改动。
func legacyClipBashOutput(ctx context.Context, raw string) string {
	limit := outputLimit(ctx)
	if limit <= 0 {
		limit = maxToolOutput
	}
	clipped := clip(ctx, sanitizeShellOutput(raw))
	if len(raw) <= limit {
		return clipped
	}
	return fmt.Sprintf("[output: ~%.1fk tokens, clipped]\n%s",
		float64(bashOutputTokens(raw))/1000, clipped)
}

// TestClipBashOutputCollapseOptInOffByteIdentical 验证 opt-in 关闭（包括未设置、
// 空串、0、false、乱写值）时 clipBashOutput 与旧实现逐字节一致。
func TestClipBashOutputCollapseOptInOffByteIdentical(t *testing.T) {
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 512})
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("line-%03d: ok", i))
	}
	lines = append(lines, "ERROR: fatal")
	raw := strings.Join(lines, "\n") + "\n"

	for _, val := range []string{"", "0", "false", " OFF "} {
		t.Run(fmt.Sprintf("env=%q", val), func(t *testing.T) {
			t.Setenv("ARGENT_OUTPUT_COLLAPSE", val)
			got := clipBashOutput(ctx, raw)
			want := legacyClipBashOutput(ctx, raw)
			if got != want {
				t.Fatalf("opt-in off changed output: got %q, want %q", got, want)
			}
			if strings.Contains(got, "earlier lines") {
				t.Fatalf("opt-in off emitted a collapse marker: %q", got)
			}
		})
	}
}

// TestClipBashOutputCollapseOptInOnKeepsTailErrorsAndMarker 验证 opt-in 开启时
// 行折叠保留尾部错误行与头部上下文，并插入计数正确的 [N earlier lines] 标记。
func TestClipBashOutputCollapseOptInOnKeepsTailErrorsAndMarker(t *testing.T) {
	t.Setenv("ARGENT_OUTPUT_COLLAPSE", "1")
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 4000})

	var lines []string
	for i := 0; i < 208; i++ {
		lines = append(lines, fmt.Sprintf("L%03d ok", i))
	}
	lines = append(lines, "ERROR: fatal", "ERROR: last")
	raw := strings.Join(lines, "\n") + "\n" // 210 行

	got := clipBashOutput(ctx, raw)

	// 210 行触发行折叠：行预算 199 = 49 头 + 150 尾，省略 11 行。
	const wantEarlier = 11
	m := collapseMarkerRE.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("missing [N earlier lines] marker: %q", got[:min(200, len(got))])
	}
	if m[1] != fmt.Sprint(wantEarlier) {
		t.Fatalf("marker N=%s, want %d", m[1], wantEarlier)
	}
	if !strings.HasSuffix(got, "ERROR: fatal\nERROR: last\n") {
		t.Fatalf("tail error lines not preserved: %q", got[max(0, len(got)-80):])
	}
	if !strings.Contains(got, "L000 ok") {
		t.Fatalf("head context line missing: %q", got[:min(120, len(got))])
	}
	// EarlyLines 字段必须与标记中的 N 一致。
	res := collapseOutput(sanitizeShellOutput(raw), 0, 4000)
	if res.EarlyLines != wantEarlier {
		t.Fatalf("EarlyLines=%d, want %d", res.EarlyLines, wantEarlier)
	}
}

// TestCollapseOutputCJKEmojiRunesSafe 验证 CJK/emoji 场景下折叠结果仍是合法
// UTF-8，且不引入替换字符（切点全部落在 rune 边界）。
func TestCollapseOutputCJKEmojiRunesSafe(t *testing.T) {
	cases := []struct {
		name     string
		s        string
		maxLines int
		maxChars int
	}{
		{"single long line", strings.Repeat("中文测试😀", 80), 6, 40},
		{"multi lines", strings.Repeat("日本語テキスト🚀", 60) + "\n" + strings.Repeat("错误", 20), 5, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collapseOutput(tc.s, tc.maxLines, tc.maxChars)
			if !utf8.ValidString(got.Text) {
				t.Fatalf("result is not valid UTF-8: %q", got.Text)
			}
			if strings.ContainsRune(got.Text, utf8.RuneError) {
				t.Fatalf("result contains a broken rune: %q", got.Text)
			}
			if !collapseMarkerRE.MatchString(got.Text) {
				t.Fatalf("expected a collapse marker in %q", got.Text)
			}
		})
	}
}

// TestClipBashOutputCollapseOptInOnOverlongTruncatesSafely 验证超长单行（含 CJK/emoji）
// 在 opt-in 下仍被安全截断：尾部错误信息保留、UTF-8 合法、字符预算不失控。
func TestClipBashOutputCollapseOptInOnOverlongTruncatesSafely(t *testing.T) {
	t.Setenv("ARGENT_OUTPUT_COLLAPSE", "1")
	const limit = 120
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: limit})
	raw := strings.Repeat("ERR ", 400) + "中文致命错误😀\n"

	got := clipBashOutput(ctx, raw)
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "中文致命错误😀") {
		t.Fatalf("tail error (with CJK/emoji) was lost: %q", got)
	}
	if !strings.Contains(got, "earlier lines]") {
		t.Fatalf("missing collapse marker: %q", got)
	}
	// 去掉体量提示行后，折叠正文的字符数不应超过预算加标记宽度。
	_, body, ok := strings.Cut(got, "\n")
	if !ok {
		t.Fatalf("missing volume hint line: %q", got)
	}
	if n := utf8.RuneCountInString(body); n > limit+32 {
		t.Fatalf("collapsed body has %d chars, over budget %d: %q", n, limit, body)
	}
}
