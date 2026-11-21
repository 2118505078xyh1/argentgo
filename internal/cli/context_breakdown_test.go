package cli

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

func TestBreakdownContextProportions(t *testing.T) {
	segs := BreakdownContext(1000, 2000, 800, 200)

	if len(segs) != 4 {
		t.Fatalf("len(segs) = %d, want 4", len(segs))
	}
	wantTokens := []int{1000, 2000, 800, 200}
	wantPct := []float64{25, 50, 20, 5}
	for i, seg := range segs {
		if seg.Tokens != wantTokens[i] {
			t.Errorf("segs[%d].Tokens = %d, want %d", i, seg.Tokens, wantTokens[i])
		}
		if diff := seg.Percent - wantPct[i]; diff < -1e-9 || diff > 1e-9 {
			t.Errorf("segs[%d].Percent = %v, want %v", i, seg.Percent, wantPct[i])
		}
	}
	// 四类占比之和应为 100%。
	sum := 0.0
	for _, seg := range segs {
		sum += seg.Percent
	}
	if diff := sum - 100; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("percent sum = %v, want 100", sum)
	}
}

func TestBreakdownContextZeroDenominator(t *testing.T) {
	segs := BreakdownContext(0, 0, 0, 0)
	if len(segs) != 4 {
		t.Fatalf("len(segs) = %d, want 4", len(segs))
	}
	for i, seg := range segs {
		if seg.Tokens != 0 || seg.Percent != 0 {
			t.Errorf("segs[%d] = %+v, want zero tokens and percent", i, seg)
		}
	}
}

func TestBreakdownContextStableOrder(t *testing.T) {
	segs := BreakdownContext(1, 2, 3, 4)
	wantNames := []string{contextSegSystem, contextSegSchema, contextSegHistory, contextSegFiles}
	gotNames := make([]string, len(segs))
	for i, seg := range segs {
		gotNames[i] = seg.Name
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("names = %v, want %v", gotNames, wantNames)
	}
}

func TestBreakdownContextCJKSafe(t *testing.T) {
	// token 数由调用方用 tokenize.Estimate 折算；CJK 文本折算后的结果
	// 传给分解函数，输出仍应是合法的含中文分类名的视图。
	inputs := [4]int{
		tokenize.Estimate("你是一个有用的助手"),
		tokenize.Estimate("工具定义：读取文件"),
		tokenize.Estimate("历史消息记录"),
		tokenize.Estimate("文件内容"),
	}
	for _, n := range inputs {
		if n <= 0 {
			t.Fatalf("tokenize.Estimate returned %d for CJK text, want > 0", n)
		}
	}
	segs := BreakdownContext(inputs[0], inputs[1], inputs[2], inputs[3])
	if len(segs) != 4 {
		t.Fatalf("len(segs) = %d, want 4", len(segs))
	}
	for i, seg := range segs {
		if seg.Tokens != inputs[i] {
			t.Errorf("segs[%d].Tokens = %d, want %d", i, seg.Tokens, inputs[i])
		}
		if !utf8.ValidString(seg.Name) || !strings.ContainsAny(seg.Name, "系统工具历史文件") {
			t.Errorf("segs[%d].Name = %q, want valid Chinese name", i, seg.Name)
		}
	}
}

func TestBreakdownContextIsPure(t *testing.T) {
	// 相同入参得到相同结果，且两次调用返回彼此独立的新切片。
	a := BreakdownContext(10, 20, 30, 40)
	b := BreakdownContext(10, 20, 30, 40)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two calls differ: %v vs %v", a, b)
	}
	a[0].Tokens = -999
	if b[0].Tokens != 10 {
		t.Fatalf("mutating first result affected second: %v", b)
	}
}

func TestFormatContextBreakdownStable(t *testing.T) {
	segs := BreakdownContext(1000, 2000, 800, 200)
	first := FormatContextBreakdown(segs)
	second := FormatContextBreakdown(segs)
	if first != second {
		t.Fatalf("FormatContextBreakdown is not stable:\n%q\nvs\n%q", first, second)
	}

	// 四类、四位整数与一位小数百分比都应以中文表格呈现。
	for _, needle := range []string{"上下文占用分解", contextSegSystem, contextSegSchema,
		contextSegHistory, contextSegFiles, "1000", "2000", "25.0%", "50.0%", "20.0%", "5.0%"} {
		if !strings.Contains(first, needle) {
			t.Errorf("formatted breakdown = %q, want %q", first, needle)
		}
	}
}

func TestFormatContextBreakdownGolden(t *testing.T) {
	segs := BreakdownContext(1000, 2000, 800, 200)
	want := "上下文占用分解\n" +
		"分类      Tokens   占比\n" +
		"系统提示    1000  25.0%\n" +
		"工具定义    2000  50.0%\n" +
		"历史消息     800  20.0%\n" +
		"文件内容     200   5.0%\n"
	if got := FormatContextBreakdown(segs); got != want {
		t.Fatalf("FormatContextBreakdown() =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatContextBreakdownEmpty(t *testing.T) {
	got := FormatContextBreakdown(nil)
	if !strings.Contains(got, "上下文占用分解") || !strings.Contains(got, "无数据") {
		t.Fatalf("empty breakdown = %q, want title and 无数据", got)
	}
}

func TestFormatContextBreakdownCJKSafe(t *testing.T) {
	got := FormatContextBreakdown(BreakdownContext(1, 2, 3, 4))
	if !utf8.ValidString(got) {
		t.Fatalf("formatted breakdown contains invalid UTF-8: %q", got)
	}

	// 百分比必须恰好一位小数。
	pct := regexp.MustCompile(`\d+\.\d%`)
	if !pct.MatchString(got) {
		t.Fatalf("formatted breakdown = %q, want one-decimal percentages", got)
	}

	// 各分类行的首个数字应落在同一显示列（CJK 按两列计），说明中文
	// 名称与空格补齐没有破坏表格对齐。
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) < 5 {
		t.Fatalf("formatted breakdown has %d lines, want >= 5", len(lines))
	}
	wantCol := -1
	for _, name := range []string{contextSegSystem, contextSegSchema, contextSegHistory, contextSegFiles} {
		found := false
		for _, line := range lines {
			if !strings.HasPrefix(line, name) {
				continue
			}
			found = true
			col := firstDigitColumn(line)
			if wantCol == -1 {
				wantCol = col
			} else if col != wantCol {
				t.Fatalf("line %q: first digit at column %d, want %d", line, col, wantCol)
			}
		}
		if !found {
			t.Errorf("no line starts with %q", name)
		}
	}
}

// firstDigitColumn 返回行中首个数字所在的显示列（CJK 按两列计），无数字返回 -1。
func firstDigitColumn(line string) int {
	col := 0
	for _, r := range line {
		if r >= '0' && r <= '9' {
			return col
		}
		if isFullWidth(r) {
			col += 2
		} else {
			col++
		}
	}
	return -1
}

func TestFormatContextBreakdownDoesNotModifyInput(t *testing.T) {
	segs := BreakdownContext(7, 11, 13, 17)
	before := append([]ContextSegment(nil), segs...)
	_ = FormatContextBreakdown(segs)
	if !reflect.DeepEqual(segs, before) {
		t.Fatalf("FormatContextBreakdown modified its input: %v vs %v", segs, before)
	}
}

func TestContextHealthTiers(t *testing.T) {
	// 分解视图 token 总和为 65，窗口决定占用百分比与分档。
	segs := BreakdownContext(10, 20, 25, 10) // 总和 65
	cases := []struct {
		name   string
		window int
		want   string
	}{
		{"ample", 120, "窗口充裕"},            // 65/120 ≈ 54%
		{"near lower bound", 109, "占用偏高"}, // 65/109 ≈ 60%，四舍五入入 60% 档
		{"elevated", 80, "占用偏高"},
		{"near limit", 70, "接近上限"}, // 65/70 ≈ 93%
		{"over limit", 50, "已超出窗口上限"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ContextHealth(segs, tc.window)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("ContextHealth(window=%d) = %q, want %q", tc.window, got, tc.want)
			}
			if !strings.Contains(got, "上下文占用") {
				t.Fatalf("ContextHealth(window=%d) = %q, want 上下文占用", tc.window, got)
			}
		})
	}
}

func TestContextHealthExactPercent(t *testing.T) {
	segs := BreakdownContext(10, 20, 20, 0) // 总和 50
	got := ContextHealth(segs, 100)
	if !strings.Contains(got, "上下文占用 50%") {
		t.Fatalf("ContextHealth = %q, want 上下文占用 50%%", got)
	}
}

func TestContextHealthNoWindow(t *testing.T) {
	segs := BreakdownContext(1, 2, 3, 4)
	for _, window := range []int{0, -1, -100} {
		got := ContextHealth(segs, window)
		if got != "上下文窗口未知，无法评估占用" {
			t.Fatalf("ContextHealth(window=%d) = %q, want fixed unknown-window text", window, got)
		}
	}
}

func TestContextHealthCJKSafeBounded(t *testing.T) {
	// 即使 token 数极大，文案仍是合法 UTF-8 的中文且长度有界。
	segs := BreakdownContext(1<<60, 1<<60, 1<<60, 1<<60)
	got := ContextHealth(segs, 3)
	if !utf8.ValidString(got) || !strings.Contains(got, "上下文占用") {
		t.Fatalf("ContextHealth = %q, want valid Chinese text", got)
	}
	if len(got) > 128 {
		t.Fatalf("ContextHealth length = %d, want <= 128", len(got))
	}
}

func TestContextHealthDoesNotModifyInput(t *testing.T) {
	segs := BreakdownContext(7, 11, 13, 17)
	before := append([]ContextSegment(nil), segs...)
	_ = ContextHealth(segs, 100)
	if !reflect.DeepEqual(segs, before) {
		t.Fatalf("ContextHealth modified its input: %v vs %v", segs, before)
	}
}
