package llm

import (
	"math"
	"reflect"
	"testing"
)

// TestCacheStats 覆盖正常命中率计算，以及零值、负值、越界与极端整数边界。
// input 的计费口径包含缓存读取与写入，因此读取越界必须按总计截断，
// 以保证命中率落在 0..1 且各 token 计数都有界。
func TestCacheStats(t *testing.T) {
	cases := []struct {
		name      string
		input     int
		read      int
		write     int
		wantRate  float64
		wantRead  int
		wantWrite int
		wantTotal int
	}{
		{
			name: "全零输入返回全零统计",
		},
		{
			name:      "部分命中给出对应命中率",
			input:     100,
			read:      40,
			write:     10,
			wantRate:  0.4,
			wantRead:  40,
			wantWrite: 10,
			wantTotal: 100,
		},
		{
			name:      "全部命中命中率为一",
			input:     100,
			read:      100,
			wantRate:  1,
			wantRead:  100,
			wantTotal: 100,
		},
		{
			name:      "仅写入缓存不产生命中",
			input:     100,
			write:     20,
			wantWrite: 20,
			wantTotal: 100,
		},
		{
			name:      "读取越界按总计截断",
			input:     10,
			read:      25,
			write:     3,
			wantRate:  1,
			wantRead:  10,
			wantWrite: 3,
			wantTotal: 10,
		},
		{
			name:      "读取与写入同时越界",
			input:     5,
			read:      100,
			write:     100,
			wantRate:  1,
			wantRead:  5,
			wantWrite: 5,
			wantTotal: 5,
		},
		{
			name:  "全部负值归零",
			input: -5,
			read:  -3,
			write: -2,
		},
		{
			name:      "负读取归零但写入保留",
			input:     100,
			read:      -5,
			write:     3,
			wantWrite: 3,
			wantTotal: 100,
		},
		{
			name:      "负写入归零但读取保留",
			input:     100,
			read:      30,
			write:     -7,
			wantRate:  0.3,
			wantRead:  30,
			wantTotal: 100,
		},
		{
			name:  "负总计导致缓存计数被截断为零",
			input: -100,
			read:  5,
			write: 5,
		},
		{
			name:  "零总计但缓存计数非零不产生 NaN",
			input: 0,
			read:  5,
			write: 2,
		},
		{
			name:      "除不尽时保留浮点精度",
			input:     3,
			read:      1,
			write:     1,
			wantRate:  1.0 / 3.0,
			wantRead:  1,
			wantWrite: 1,
			wantTotal: 3,
		},
		{
			name:      "最大整数不溢出",
			input:     math.MaxInt,
			read:      math.MaxInt,
			write:     math.MaxInt,
			wantRate:  1,
			wantRead:  math.MaxInt,
			wantWrite: math.MaxInt,
			wantTotal: math.MaxInt,
		},
		{
			name:  "最小整数归零",
			input: math.MinInt,
			read:  math.MinInt,
			write: math.MinInt,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cacheStats(tc.input, tc.read, tc.write)
			want := map[string]any{
				"hitRate":     tc.wantRate,
				"readTokens":  tc.wantRead,
				"writeTokens": tc.wantWrite,
				"totalTokens": tc.wantTotal,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("cacheStats(%d, %d, %d) = %#v, want %#v",
					tc.input, tc.read, tc.write, got, want)
			}
		})
	}
}

// TestCacheStatsKeysAndTypes 锁定返回 map 的键集合与取值类型：报告消费方
// 依赖这些稳定的 ASCII 键名（例如编码为 JSON），键名与 CJK 区域设置无关。
func TestCacheStatsKeysAndTypes(t *testing.T) {
	stats := cacheStats(100, 25, 5)
	if len(stats) != 4 {
		t.Fatalf("键数量 = %d, want 4 (%#v)", len(stats), stats)
	}
	if rate, ok := stats["hitRate"].(float64); !ok || rate != 0.25 {
		t.Errorf("hitRate = %#v (%T), want float64(0.25)", stats["hitRate"], stats["hitRate"])
	}
	for _, key := range []string{"readTokens", "writeTokens", "totalTokens"} {
		if _, ok := stats[key].(int); !ok {
			t.Errorf("%s = %#v (%T), want int", key, stats[key], stats[key])
		}
	}
}

// TestCacheStatsStableAcrossLocales 证明结果稳定且与 CJK 环境无关：
// 切换区域设置不改变任何键值，重复调用也返回完全相同的 map。
func TestCacheStatsStableAcrossLocales(t *testing.T) {
	baseline := cacheStats(200, 50, 20)
	for _, lang := range []string{"", "zh_CN.UTF-8", "en_US.UTF-8", "ja_JP.UTF-8"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("LANG", lang)
			t.Setenv("LC_ALL", lang)
			for i := 0; i < 3; i++ {
				if got := cacheStats(200, 50, 20); !reflect.DeepEqual(got, baseline) {
					t.Fatalf("cacheStats 在 LANG=%q 第 %d 次调用 = %#v, want %#v",
						lang, i, got, baseline)
				}
			}
		})
	}
}

// TestCacheStatsIsBounded 对一组含负数与越界的入参做性质检查：
// 命中率始终是 0..1 的有限值，各 token 计数非负且不超过总计。
func TestCacheStatsIsBounded(t *testing.T) {
	inputs := []int{math.MinInt, -7, -1, 0, 1, 3, 10, 100, math.MaxInt}
	counts := []int{math.MinInt, -3, 0, 1, 9, 100, math.MaxInt}
	for _, input := range inputs {
		for _, read := range counts {
			for _, write := range counts {
				stats := cacheStats(input, read, write)
				rate := stats["hitRate"].(float64)
				if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1 {
					t.Fatalf("cacheStats(%d, %d, %d) 命中率越界: %v", input, read, write, rate)
				}
				total := stats["totalTokens"].(int)
				readTokens := stats["readTokens"].(int)
				writeTokens := stats["writeTokens"].(int)
				if total < 0 || readTokens < 0 || writeTokens < 0 ||
					readTokens > total || writeTokens > total {
					t.Fatalf("cacheStats(%d, %d, %d) token 计数越界: %#v",
						input, read, write, stats)
				}
			}
		}
	}
}
