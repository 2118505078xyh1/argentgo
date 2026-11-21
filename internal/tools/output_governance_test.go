package tools

import (
	"math"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

func TestPlanOutputClippedBoundaries(t *testing.T) {
	const limit = 16
	cases := []struct {
		name        string
		raw         string
		limit       int
		wantLimit   int
		wantClipped bool
	}{
		{"empty raw", "", limit, limit, false},
		{"exactly at limit", strings.Repeat("x", limit), limit, limit, false},
		{"one byte over limit", strings.Repeat("x", limit+1), limit, limit, true},
		{"far over limit", strings.Repeat("x", limit*2), limit, limit, true},
		{"one byte limit exact", "a", 1, 1, false},
		{"one byte limit exceeded", "ab", 1, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanOutput(tc.raw, tc.limit, false)
			if got.Limit != tc.wantLimit {
				t.Errorf("Limit = %d, want %d", got.Limit, tc.wantLimit)
			}
			if got.RawBytes != len(tc.raw) {
				t.Errorf("RawBytes = %d, want %d", got.RawBytes, len(tc.raw))
			}
			if got.Clipped != tc.wantClipped {
				t.Errorf("Clipped = %v, want %v", got.Clipped, tc.wantClipped)
			}
			if got.Spilled {
				t.Error("Spilled = true with spill disabled")
			}
		})
	}
}

func TestPlanOutputNormalizesNonPositiveLimit(t *testing.T) {
	raw := strings.Repeat("a", maxToolOutput+1)
	for _, limit := range []int{0, -1, -100} {
		got := PlanOutput(raw, limit, false)
		if got.Limit != maxToolOutput {
			t.Errorf("PlanOutput(limit=%d).Limit = %d, want %d", limit, got.Limit, maxToolOutput)
		}
		if !got.Clipped {
			t.Errorf("PlanOutput(limit=%d).Clipped = false, want true (raw exceeds default)", limit)
		}
	}
	if got := PlanOutput("short", 0, false); got.Clipped || got.Limit != maxToolOutput {
		t.Errorf("short output under default limit: %+v", got)
	}
	if got := PlanOutput("short", math.MaxInt, false); got.Clipped {
		t.Errorf("max limit must never clip: %+v", got)
	}
}

func TestPlanOutputSpillOptIn(t *testing.T) {
	atThreshold := strings.Repeat("x", DefaultOutputSpillThreshold)
	overThreshold := strings.Repeat("x", DefaultOutputSpillThreshold+1)

	cases := []struct {
		name        string
		raw         string
		enabled     bool
		wantSpilled bool
	}{
		{"disabled at threshold", atThreshold, false, false},
		{"disabled over threshold", overThreshold, false, false},
		{"enabled at threshold", atThreshold, true, false},
		{"enabled over threshold", overThreshold, true, true},
		{"enabled small output", "small", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanOutput(tc.raw, 0, tc.enabled)
			if got.Spilled != tc.wantSpilled {
				t.Errorf("Spilled = %v, want %v", got.Spilled, tc.wantSpilled)
			}
		})
	}

	// 超过默认落盘阈值的输出必然也超过默认裁剪上限，两个标志可以同时为真。
	got := PlanOutput(overThreshold, 0, true)
	if !got.Clipped || !got.Spilled {
		t.Errorf("want both Clipped and Spilled for huge output, got %+v", got)
	}
}

func TestPlanOutputCJKAndEmoji(t *testing.T) {
	cjk := strings.Repeat("中", 100)  // 100 个宽 rune，300 字节
	emoji := strings.Repeat("😀", 50) // 50 个宽 rune，200 字节
	mixed := "ab中文😀"

	cases := []struct {
		name       string
		raw        string
		wantBytes  int
		wantTokens int
	}{
		{"cjk", cjk, 300, 100},
		{"emoji", emoji, 200, 50},
		{"mixed", mixed, len(mixed), 4}, // ab -> 1, 中/文/😀 -> 3
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanOutput(tc.raw, 4096, false)
			if got.RawBytes != tc.wantBytes {
				t.Errorf("RawBytes = %d, want %d", got.RawBytes, tc.wantBytes)
			}
			if got.TokenHint != tc.wantTokens {
				t.Errorf("TokenHint = %d, want %d", got.TokenHint, tc.wantTokens)
			}
		})
	}

	// Clipped 按字节而非 rune 判定：300 字节 / 100 rune 的中文字串在
	// 200 字节上限下必须裁剪，在恰好 300 字节时不得裁剪。
	if got := PlanOutput(cjk, 200, false); !got.Clipped {
		t.Errorf("300-byte CJK output over 200-byte limit must clip: %+v", got)
	}
	if got := PlanOutput(cjk, 300, false); got.Clipped {
		t.Errorf("300-byte CJK output at 300-byte limit must not clip: %+v", got)
	}
}

func TestPlanOutputTokenHint(t *testing.T) {
	if got := PlanOutput("", 10, false).TokenHint; got != 0 {
		t.Errorf("empty TokenHint = %d, want 0", got)
	}
	if got := PlanOutput(strings.Repeat("a", 400), 1000, false).TokenHint; got != 100 {
		t.Errorf("400 ASCII chars TokenHint = %d, want 100", got)
	}
	if got := PlanOutput(strings.Repeat("中", 100), 1000, false).TokenHint; got != 100 {
		t.Errorf("100 CJK chars TokenHint = %d, want 100", got)
	}
	if got := PlanOutput("\U0001F600\U0001F600", 1000, false).TokenHint; got != 2 {
		t.Errorf("two emoji TokenHint = %d, want 2", got)
	}

	// 与共享估算器逐例对齐，确保诊断结果不会随实现漂移。
	for _, raw := range []string{"", "hello world", strings.Repeat("a", 400), strings.Repeat("中", 100)} {
		if got, want := PlanOutput(raw, 1000, false).TokenHint, tokenize.Estimate(raw); got != want {
			t.Errorf("TokenHint(%q) = %d, want tokenize.Estimate = %d", raw, got, want)
		}
	}
}

func TestPlanOutputDescribeStableAndBounded(t *testing.T) {
	b := PlanOutput("abc中文😀", 4, true)
	d1 := b.Describe()
	if d1 != b.Describe() {
		t.Error("Describe is not stable across repeated calls")
	}
	if same := PlanOutput("abc中文😀", 4, true); same != b || same.Describe() != d1 {
		t.Errorf("equal raw must yield equal budget and summary: %+v vs %+v", same, b)
	}
	if len(d1) > 128 {
		t.Errorf("Describe length = %d, want <= 128: %q", len(d1), d1)
	}
	for _, frag := range []string{"原始 ", " 字节", "上限 ", "tokens", "需要裁剪", "不落盘"} {
		if !strings.Contains(d1, frag) {
			t.Errorf("Describe = %q, missing %q", d1, frag)
		}
	}
	spillable := PlanOutput(strings.Repeat("x", DefaultOutputSpillThreshold+1), 0, true).Describe()
	if !strings.Contains(spillable, "可落盘") {
		t.Errorf("spillable Describe = %q, missing 可落盘", spillable)
	}
	// 摘要不得嵌入原始输出内容。
	for _, secret := range []string{"中文", "😀", "abc"} {
		if strings.Contains(d1, secret) {
			t.Errorf("Describe leaks raw content %q: %q", secret, d1)
		}
	}

	plain := PlanOutput("a", 100, false).Describe()
	if !strings.Contains(plain, "无需裁剪") || !strings.Contains(plain, "不落盘") {
		t.Errorf("plain Describe = %q, want 无需裁剪/不落盘", plain)
	}
	if plain == d1 {
		t.Error("different budgets must produce different summaries")
	}
}

func TestPlanOutputPureDoesNotMutateInput(t *testing.T) {
	data := []byte("中😀abc")
	raw := string(data)
	before := string(data)
	limit := 5

	got1 := PlanOutput(raw, limit, true)
	if string(data) != before {
		t.Errorf("backing bytes mutated: %q, want %q", data, before)
	}
	if raw != before {
		t.Errorf("raw mutated: %q, want %q", raw, before)
	}
	if limit != 5 {
		t.Errorf("limit mutated to %d", limit)
	}

	got2 := PlanOutput(raw, limit, true)
	if got1 != got2 {
		t.Errorf("repeated call changed result: %+v vs %+v", got1, got2)
	}
	// 内容相同但底层存储不同的字符串必须得到完全相同的预算，证明无隐藏状态。
	if got3 := PlanOutput(strings.Clone(raw), limit, true); got3 != got1 {
		t.Errorf("fresh equal string changed result: %+v vs %+v", got3, got1)
	}
}

func TestPlanOutputFieldInvariants(t *testing.T) {
	raws := []string{"", "a", strings.Repeat("x", 100), strings.Repeat("中", 100)}
	limits := []int{-1, 0, 1, 100, math.MaxInt}
	for _, raw := range raws {
		for _, limit := range limits {
			for _, enabled := range []bool{false, true} {
				b := PlanOutput(raw, limit, enabled)
				effective := limit
				if effective <= 0 {
					effective = maxToolOutput
				}
				if b.RawBytes != len(raw) || b.Limit != effective {
					t.Errorf("PlanOutput(%d bytes, %d, %v): raw=%d limit=%d", len(raw), limit, enabled, b.RawBytes, b.Limit)
				}
				if b.Clipped != (b.RawBytes > b.Limit) {
					t.Errorf("Clipped inconsistent: %+v", b)
				}
				if b.Spilled != (enabled && b.RawBytes > DefaultOutputSpillThreshold) {
					t.Errorf("Spilled inconsistent: %+v", b)
				}
				if b.TokenHint != tokenize.Estimate(raw) {
					t.Errorf("TokenHint inconsistent: %+v", b)
				}
			}
		}
	}
}
