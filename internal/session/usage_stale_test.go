package session

import "testing"

func TestIsStaleUsage(t *testing.T) {
	cases := []struct {
		name             string
		usageTS          int64
		lastCompactionTS int64
		isError          bool
		want             bool
	}{
		// 错误回复无条件陈旧：即便时间戳很新也不信。
		{"error reply is stale regardless of timestamps", 100, 50, true, true},
		{"error reply with zero timestamps is stale", 0, 0, true, true},
		{"error reply with negative timestamps is stale", -10, -20, true, true},
		// 用量早于压缩：陈旧。
		{"usage before compaction", 40, 50, false, true},
		{"usage one before compaction", 49, 50, false, true},
		{"usage far before compaction", -100, 50, false, true},
		{"usage zero with real compaction", 0, 50, false, true},
		{"negative usage with real compaction", -1, 1, false, true},
		// 用量晚于压缩：新鲜。
		{"usage after compaction", 60, 50, false, false},
		{"usage one after compaction", 51, 50, false, false},
		{"usage far after compaction", 1_000, 50, false, false},
		// 相等边界：同一时刻，新鲜。
		{"usage equals compaction", 50, 50, false, false},
		{"both zero", 0, 0, false, false},
		{"equal negative timestamps", -5, -5, false, false},
		// 无有效压缩（<= 0）：不因压缩而陈旧，无论用量时间戳。
		{"zero compaction ignored", 100, 0, false, false},
		{"negative compaction ignored", 100, -5, false, false},
		{"negative usage with negative compaction ignored", -10, -5, false, false},
		// 单步与大数边界。
		{"single step", 1, 2, false, true},
		{"large values", 1_000_000, 2_000_000, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isStaleUsage(tc.usageTS, tc.lastCompactionTS, tc.isError)
			if got != tc.want {
				t.Fatalf("isStaleUsage(%d, %d, %v) = %v, want %v",
					tc.usageTS, tc.lastCompactionTS, tc.isError, got, tc.want)
			}
		})
	}
}

// TestIsStaleUsageIsStable 穷举一小段时间戳，断言函数是确定性的纯函数：
// 错误回复恒为陈旧；无有效压缩恒为新鲜；用量推进时陈旧性单调地由真转假、不会回退。
func TestIsStaleUsageIsStable(t *testing.T) {
	values := []int64{-3, -1, 0, 1, 7, 32, 1_000}
	for _, compactionTS := range values {
		prev := true // 从最陈旧的假设开始，确保新鲜之后不会倒退。
		for _, usageTS := range values {
			got := isStaleUsage(usageTS, compactionTS, false)
			if again := isStaleUsage(usageTS, compactionTS, false); again != got {
				t.Fatalf("非确定性：isStaleUsage(%d, %d) 两次结果不同", usageTS, compactionTS)
			}
			if !isStaleUsage(usageTS, compactionTS, true) {
				t.Fatalf("错误回复应恒为陈旧：(%d, %d)", usageTS, compactionTS)
			}
			if compactionTS <= 0 && got {
				t.Fatalf("无有效压缩时不应陈旧：(%d, %d)", usageTS, compactionTS)
			}
			if !prev && got {
				t.Fatalf("用量时间戳递增时陈旧性不应回退：(%d, %d)", usageTS, compactionTS)
			}
			prev = got
		}
	}
}
