package tokenest

import (
	"math"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

func TestCountBatchEmptyAndAlignment(t *testing.T) {
	if got := CountBatch(nil); len(got) != 0 {
		t.Fatalf("CountBatch(nil) = %v", got)
	}
	if got := CountBatch([]string{}); len(got) != 0 {
		t.Fatalf("CountBatch([]) = %v", got)
	}
	got := CountBatch([]string{"", "", "abcd"})
	want := []int{0, 0, 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CountBatch = %v, want %v", got, want)
	}
}

// TestCountBatchMatchesTokenizeOnCompatibleInputs 守护"与现有预算不冲突"：
// 纯 ASCII 与纯宽 rune 文本上，批量估算必须与 tokenize.Estimate 逐值一致。
func TestCountBatchMatchesTokenizeOnCompatibleInputs(t *testing.T) {
	texts := []string{
		"",
		"hello world, this is plain ASCII code; x := 42",
		strings.Repeat("a", 401),
		"你好世界这是中文文本",
		"こんにちは世界",
		"한국어 텍스트",
		strings.Repeat("汉", 137),
	}
	got := CountBatch(texts)
	for i, text := range texts {
		want := tokenize.Estimate(text)
		if got[i] != want {
			t.Errorf("CountBatch[%d](%q) = %d, tokenize = %d", i, text, got[i], want)
		}
	}
}

func TestCountBatchMixedOverEstimateIsSafe(t *testing.T) {
	// 混合文本允许比 tokenize 略高（高估对预算安全），但不得显著低于它。
	text := "hello 世界 123, world!"
	got := CountBatch([]string{text})[0]
	base := tokenize.Estimate(text)
	if got < base {
		t.Fatalf("mixed estimate %d < tokenize %d (would under-count)", got, base)
	}
}

func TestCountBatchParallelMatchesSerial(t *testing.T) {
	if runtime.GOMAXPROCS(0) <= 1 {
		t.Skip("需要多核才能覆盖 fan-out 路径")
	}
	texts := make([]string, 1000)
	rng := rand.New(rand.NewSource(1))
	alphabet := []rune("abcXYZ0123，。世界 !?\n")
	for i := range texts {
		n := 20 + rng.Intn(120)
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		texts[i] = b.String()
	}
	got := CountBatch(texts)
	for i, text := range texts {
		want := countSpan([]byte(text))
		if got[i] != want {
			t.Fatalf("parallel[%d] = %d, serial = %d", i, got[i], want)
		}
	}
}

func TestCountBatchBelowThresholdIsSerial(t *testing.T) {
	// 15 条以下必须走串行分支（结果相同即可；分支本身由阈值常量保护）。
	texts := make([]string, parallelBatchMin-1)
	for i := range texts {
		texts[i] = strings.Repeat("a", i+1)
	}
	got := CountBatch(texts)
	for i := range texts {
		if want := countSpan([]byte(texts[i])); got[i] != want {
			t.Fatalf("[%d] = %d, want %d", i, got[i], want)
		}
	}
}

func TestEstimateItemsAggregatesByKind(t *testing.T) {
	items := []Item{
		{Kind: "user", Text: "hello"},
		{Kind: "user", Text: "world"},
		{Kind: "assistant", Text: "你好"},
		{Kind: "tool", Text: ""},
	}
	total, byKind := EstimateItems(items)
	want := map[string]int{
		"user":      countSpan([]byte("hello")) + countSpan([]byte("world")),
		"assistant": countSpan([]byte("你好")),
		"tool":      0,
	}
	if !reflect.DeepEqual(byKind, want) {
		t.Fatalf("byKind = %v, want %v", byKind, want)
	}
	sum := 0
	for _, v := range want {
		sum += v
	}
	if total != sum {
		t.Fatalf("total = %d, want %d", total, sum)
	}
}

func TestSatAddInt(t *testing.T) {
	if got := satAddInt(math.MaxInt-1, 10); got != math.MaxInt {
		t.Fatalf("positive saturation = %d", got)
	}
	if got := satAddInt(math.MinInt+1, -10); got != math.MinInt {
		t.Fatalf("negative saturation = %d", got)
	}
	if got := satAddInt(3, 4); got != 7 {
		t.Fatalf("normal add = %d", got)
	}
}

func countAllSerial(texts []string) []int {
	out := make([]int, len(texts))
	for i, text := range texts {
		out[i] = countSpan([]byte(text))
	}
	return out
}

// TestCountBatchParallelSpeedup 是"批量 1000 条 < 串行×0.6"的 bench 断言。
// 取多次测量的最小值以压低调度噪声；多核不足时跳过（此时 CountBatch 走串行）。
func TestCountBatchParallelSpeedup(t *testing.T) {
	if testing.Short() || runtime.GOMAXPROCS(0) <= 1 {
		t.Skip("需要多核，且短模式下跳过计时断言")
	}
	texts := make([]string, 1000)
	rng := rand.New(rand.NewSource(2))
	for i := range texts {
		texts[i] = strings.Repeat("abc世界123", 20+rng.Intn(10))
	}
	// 预热，让 GC 与调度进入稳态。
	for i := 0; i < 5; i++ {
		_ = CountBatch(texts)
		_ = countAllSerial(texts)
	}
	// 单次计时受 Windows 时钟粒度影响，按多次批量计时（与 benchmark 同口径）；
	// 多轮取最小值，避开机器上的其它编译负载。
	const reps = 300
	var serial, parallel time.Duration = time.Duration(1 << 62), time.Duration(1 << 62)
	for round := 0; round < 7; round++ {
		start := time.Now()
		for i := 0; i < reps; i++ {
			_ = countAllSerial(texts)
		}
		if d := time.Since(start); d < serial {
			serial = d
		}
		start = time.Now()
		for i := 0; i < reps; i++ {
			_ = CountBatch(texts)
		}
		if d := time.Since(start); d < parallel {
			parallel = d
		}
	}
	ratio := float64(parallel) / float64(serial)
	t.Logf("serial=%v parallel=%v ratio=%.2f", serial, parallel, ratio)
	if parallel >= serial*6/10 {
		t.Fatalf("批量并行未达 0.6×：serial=%v parallel=%v", serial, parallel)
	}
}

func BenchmarkCountBatchLarge(b *testing.B) {
	texts := make([]string, 1000)
	rng := rand.New(rand.NewSource(2))
	for i := range texts {
		texts[i] = strings.Repeat("abc世界123", 20+rng.Intn(10))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CountBatch(texts)
	}
}

func BenchmarkCountBatchSerialLarge(b *testing.B) {
	texts := make([]string, 1000)
	rng := rand.New(rand.NewSource(2))
	for i := range texts {
		texts[i] = strings.Repeat("abc世界123", 20+rng.Intn(10))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, text := range texts {
			_ = countSpan([]byte(text))
		}
	}
}

// BenchmarkTokenizeEstimateSerial 是被 batch 路径替代的旧逐条估算基线。
func BenchmarkTokenizeEstimateSerial(b *testing.B) {
	texts := make([]string, 1000)
	rng := rand.New(rand.NewSource(2))
	for i := range texts {
		texts[i] = strings.Repeat("abc世界123", 20+rng.Intn(10))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, text := range texts {
			_ = tokenize.Estimate(text)
		}
	}
}
