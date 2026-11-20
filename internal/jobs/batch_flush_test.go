package jobs

import (
	"math"
	"testing"
	"time"
)

// TestShouldFlushBatchCountTrigger 验证仅启用条数阈值时按条数触发。
func TestShouldFlushBatchCountTrigger(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  bool
	}{
		{"一条未达三条", 1, false},
		{"两条未达三条", 2, false},
		{"三条恰好触发", 3, true},
		{"四条超过触发", 4, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 只开条数阈值（maxCount=3），字节与时间维度以零值关闭。
			if got := shouldFlushBatch(tt.count, 0, 0, 0, 3, 0); got != tt.want {
				t.Fatalf("shouldFlushBatch(count=%d, maxCount=3) = %v, want %v", tt.count, got, tt.want)
			}
		})
	}
}

// TestShouldFlushBatchBytesTrigger 验证仅启用字节阈值时按字节数触发。
func TestShouldFlushBatchBytesTrigger(t *testing.T) {
	tests := []struct {
		name  string
		bytes int
		want  bool
	}{
		{"99 字节未达", 99, false},
		{"100 字节恰好触发", 100, true},
		{"101 字节超过触发", 101, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 只开字节阈值（maxBytes=100），其余维度关闭。
			if got := shouldFlushBatch(0, tt.bytes, 0, 0, 0, 100); got != tt.want {
				t.Fatalf("shouldFlushBatch(bytes=%d, maxBytes=100) = %v, want %v", tt.bytes, got, tt.want)
			}
		})
	}
}

// TestShouldFlushBatchCJKIsByteBased 验证字节判定与字符宽度无关：中文按 UTF-8 字节数计入。
// "中文" 是 2 个 rune、6 个字节；阈值 5 只在按字节计数时才会触发。
func TestShouldFlushBatchCJKIsByteBased(t *testing.T) {
	const text = "中文"
	if len(text) != 6 {
		t.Fatalf("测试前置条件失败：len(%q) = %d, want 6", text, len(text))
	}
	if got := shouldFlushBatch(0, len(text), 0, 0, 0, 5); !got {
		t.Fatal("按字节计数时 6 字节应触发 maxBytes=5")
	}
	if got := shouldFlushBatch(0, 2, 0, 0, 0, 5); got {
		t.Fatal("按 rune 计数（2）不应触发 maxBytes=5，判定必须只看字节")
	}
}

// TestShouldFlushBatchTimeTrigger 验证仅启用时间阈值时按经过时长触发。
func TestShouldFlushBatchTimeTrigger(t *testing.T) {
	const maxDelay = 100 * time.Millisecond
	tests := []struct {
		name    string
		elapsed time.Duration
		want    bool
	}{
		{"远未到达", 10 * time.Millisecond, false},
		{"差一纳秒未达", maxDelay - time.Nanosecond, false},
		{"恰好到达", maxDelay, true},
		{"超过", maxDelay + time.Millisecond, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 只开时间阈值（maxDelay=100ms），其余维度关闭。
			if got := shouldFlushBatch(0, 0, tt.elapsed, maxDelay, 0, 0); got != tt.want {
				t.Fatalf("shouldFlushBatch(elapsed=%v, maxDelay=%v) = %v, want %v", tt.elapsed, maxDelay, got, tt.want)
			}
		})
	}
}

// TestShouldFlushBatchBelowThreshold 验证所有已启用维度都未达阈值时不触发。
func TestShouldFlushBatchBelowThreshold(t *testing.T) {
	got := shouldFlushBatch(9, 4095, 99*time.Millisecond, 100*time.Millisecond, 10, 4096)
	if got {
		t.Fatal("三条已启用阈值均未达到时不应触发")
	}
}

// TestShouldFlushBatchBoundary 验证每条规则的恰好边界（>= 语义）以及极大值的有界性。
func TestShouldFlushBatchBoundary(t *testing.T) {
	tests := []struct {
		name               string
		count, bytes       int
		elapsed, maxDelay  time.Duration
		maxCount, maxBytes int
		want               bool
	}{
		{"条数边界", 10, 0, 0, 0, 10, 0, true},
		{"字节边界", 0, 10, 0, 0, 0, 10, true},
		{"时间边界", 0, 0, time.Second, time.Second, 0, 0, true},
		{"条数边界前一", 9, 0, 0, 0, 10, 0, false},
		{"字节边界前一", 0, 9, 0, 0, 0, 10, false},
		{"时间边界前一", 0, 0, time.Second - time.Nanosecond, time.Second, 0, 0, false},
		{"最大整数条数不溢出", math.MaxInt, 0, 0, 0, math.MaxInt, 0, true},
		{"最小整数条数不误触发", math.MinInt, 0, 0, 0, math.MaxInt, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFlushBatch(tt.count, tt.bytes, tt.elapsed, tt.maxDelay, tt.maxCount, tt.maxBytes); got != tt.want {
				t.Fatalf("shouldFlushBatch(%d, %d, %v, %v, %d, %d) = %v, want %v",
					tt.count, tt.bytes, tt.elapsed, tt.maxDelay, tt.maxCount, tt.maxBytes, got, tt.want)
			}
		})
	}
}

// TestShouldFlushBatchZeroAndNegative 验证零值/负值阈值的关闭语义与负输入的稳健性。
func TestShouldFlushBatchZeroAndNegative(t *testing.T) {
	tests := []struct {
		name               string
		count, bytes       int
		elapsed, maxDelay  time.Duration
		maxCount, maxBytes int
		want               bool
	}{
		{"全零阈值永不触发", 100, 10000, time.Hour, 0, 0, 0, false},
		{"全负阈值永不触发", 100, 10000, time.Hour, -1, -1, -1, false},
		{"零条数不触发已启用条数阈值", 0, 0, 0, 0, 1, 0, false},
		{"负输入不触发条数阈值", -5, 0, 0, 0, 1, 0, false},
		{"负字节数不触发字节阈值", 0, -5, 0, 0, 0, 1, false},
		{"负时长不触发时间阈值", 0, 0, -time.Second, time.Millisecond, 0, 0, false},
		{"仅条数启用时字节阈值关闭", 0, math.MaxInt, time.Hour, 0, 3, -1, false},
		{"仅条数启用且达到", 3, math.MaxInt, time.Hour, 0, 3, -1, true},
		{"负阈值不影响其他维度", 1, 0, 0, 0, 1, -1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFlushBatch(tt.count, tt.bytes, tt.elapsed, tt.maxDelay, tt.maxCount, tt.maxBytes); got != tt.want {
				t.Fatalf("shouldFlushBatch(%d, %d, %v, %v, %d, %d) = %v, want %v",
					tt.count, tt.bytes, tt.elapsed, tt.maxDelay, tt.maxCount, tt.maxBytes, got, tt.want)
			}
		})
	}
}
