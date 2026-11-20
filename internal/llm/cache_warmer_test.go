package llm

import (
	"testing"
	"time"
)

// TestShouldWarmCache 固定纯保活决策：只有在缓存生存期的最后五分之一、
// 过期之前、30 分钟空闲上限之内，且仅当预期节省的未命中成本
// 超过保活成本时，保活才有资格。
func TestShouldWarmCache(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return base.Add(d) }
	hour := at(time.Hour)

	cases := []struct {
		name      string
		now       time.Time
		ttl       time.Time
		idleSince time.Time
		warmCost  float64
		missCost  float64
		prob      float64
		want      bool
	}{
		{
			name:      "fresh request is not warmed",
			now:       at(time.Minute),
			ttl:       hour,
			idleSince: base,
			warmCost:  1, missCost: 10, prob: 0.5,
			want: false,
		},
		{
			name:      "before the warm window",
			now:       at(47 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0.5,
			want: false,
		},
		{
			name:      "at the 80 percent window start",
			now:       at(48 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0.5,
			want: true,
		},
		{
			name:      "at the pi 90 percent trigger",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0.5,
			want: true,
		},
		{
			name:      "at the expiry boundary",
			now:       hour,
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0.5,
			want: true,
		},
		{
			name:      "past expiry",
			now:       hour.Add(time.Nanosecond),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0.5,
			want: false,
		},
		{
			name:      "idle beyond the 30 minute cap",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(20 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0.5,
			want: false,
		},
		{
			name:      "idle exactly at the 30 minute cap",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(24 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0.5,
			want: true,
		},
		{
			name:      "zero idleSince skips the idle cap",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: time.Time{},
			warmCost:  1, missCost: 10, prob: 0.5,
			want: true,
		},
		{
			name:      "warm costs more than the expected saving",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  6, missCost: 10, prob: 0.5,
			want: false,
		},
		{
			name:      "break even is not warmed",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  5, missCost: 10, prob: 0.5,
			want: false,
		},
		{
			name:      "no continuation chance",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: 10, prob: 0,
			want: false,
		},
		{
			name:      "full continuation probability",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: 10, prob: 1,
			want: true,
		},
		{
			name:      "free warm with a real miss cost",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  0, missCost: 10, prob: 0.15,
			want: true,
		},
		{
			name:      "negative miss cost",
			now:       at(54 * time.Minute),
			ttl:       hour,
			idleSince: at(45 * time.Minute),
			warmCost:  1, missCost: -10, prob: 1,
			want: false,
		},
		{
			name:      "deadline before last request",
			now:       at(-30 * time.Second),
			ttl:       at(-time.Minute),
			idleSince: base,
			warmCost:  1, missCost: 10, prob: 1,
			want: false,
		},
		{
			name:      "zero lifetime",
			now:       base,
			ttl:       base,
			idleSince: base,
			warmCost:  1, missCost: 10, prob: 1,
			want: false,
		},
		{
			name:      "now before last request",
			now:       at(-time.Minute),
			ttl:       hour,
			idleSince: base,
			warmCost:  1, missCost: 10, prob: 1,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldWarmCache(tc.now, base, tc.ttl, tc.idleSince, tc.warmCost, tc.missCost, tc.prob)
			if got != tc.want {
				t.Fatalf("ShouldWarmCache() = %v, want %v", got, tc.want)
			}
		})
	}
}
