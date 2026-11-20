package agent

import "testing"

func TestCompactionThreshold(t *testing.T) {
	cases := []struct {
		name          string
		contextWindow int
		configured    int
		percent       int
		want          int
	}{
		// A positive percent wins over a configured token threshold.
		{"percent wins", 100_000, 5_000, 50, 45_000},
		{"percent with no configured threshold", 200_000, 0, 25, 42_000},
		// -1 and 0 inherit the configured token threshold.
		{"minus one inherits configured", 100_000, 5_000, -1, 5_000},
		{"zero percent inherits configured", 100_000, 5_000, 0, 5_000},
		{"minus one with no configured falls back to default", 100_000, 0, -1, defaultCompactionTokens},
		// Configured value is used when no percentage applies.
		{"configured used", 0, 12_345, 0, 12_345},
		{"configured wins over negative percent", 0, 7_777, -5, 7_777},
		// No inputs at all: compiled default.
		{"default fallback", 0, 0, 0, defaultCompactionTokens},
		{"default fallback with window but no percent", 128_000, 0, 0, defaultCompactionTokens},
		// Reserved output buffer is capped at 8000 tokens.
		{"reserve capped at 8000", 1_000_000, 0, 80, 792_000},
		{"reserve is five percent below cap", 100_000, 0, 50, 45_000},
		// Small windows never underflow below one.
		{"small window clamps to one", 100, 0, 1, 1},
		{"tiny window clamps to one", 5, 0, 10, 1},
		{"single token window", 1, 0, 100, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactionThreshold(tc.contextWindow, tc.configured, tc.percent); got != tc.want {
				t.Fatalf("CompactionThreshold(%d, %d, %d) = %d, want %d",
					tc.contextWindow, tc.configured, tc.percent, got, tc.want)
			}
		})
	}
}

func TestInflateEstimate(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want int
	}{
		{"zero", 0, 0},
		{"negative", -5, 0},
		{"one rounds up", 1, 2},
		{"two", 2, 3},
		{"hundred", 100, 130},
		{"thousand", 1_000, 1_300},
		{"large", 1_000_000, 1_300_000},
		{"non multiple rounds up", 101, 132},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InflateEstimate(tc.n); got != tc.want {
				t.Fatalf("InflateEstimate(%d) = %d, want %d", tc.n, got, tc.want)
			}
		})
	}
}
