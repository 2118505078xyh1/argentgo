package proc

import (
	"math"
	"testing"
)

// TestRollbackOffset 覆盖追加失败回滚偏移的正常、写 0 字节、负值、超大值与
// 边界情形：目标始终是夹到非负的写前大小，且不会越过追加前的文件末尾。
func TestRollbackOffset(t *testing.T) {
	tests := []struct {
		name         string
		beforeSize   int64
		writtenBytes int64
		want         int64
	}{
		{"正常部分写入回滚到写前偏移", 100, 30, 100},
		{"整行写入后失败仍回到写前偏移", 4096, 50, 4096},
		{"写0字节返回写前偏移", 100, 0, 100},
		{"负的已写字节数按0处理", 100, -5, 100},
		{"负的写前偏移夹到0", -1, 30, 0},
		{"两者皆负夹到0", -9, -3, 0},
		{"写前大小为0且部分写入返回0", 0, 42, 0},
		{"空文件写0字节返回0", 0, 0, 0},
		{"超大写前偏移原样返回", math.MaxInt64, 1, math.MaxInt64},
		{"超大写前偏移写0字节原样返回", math.MaxInt64, 0, math.MaxInt64},
		{"超大已写字节数不影响目标", 1, math.MaxInt64, 1},
		{"最小正偏移边界", 1, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rollbackOffset(tt.beforeSize, tt.writtenBytes)
			if got != tt.want {
				t.Fatalf("rollbackOffset(%d, %d) = %d, want %d",
					tt.beforeSize, tt.writtenBytes, got, tt.want)
			}
			if got < 0 {
				t.Fatalf("rollbackOffset(%d, %d) = %d, want non-negative",
					tt.beforeSize, tt.writtenBytes, got)
			}
			if tt.beforeSize >= 0 && got > tt.beforeSize {
				t.Fatalf("rollbackOffset(%d, %d) = %d, want <= beforeSize %d",
					tt.beforeSize, tt.writtenBytes, got, tt.beforeSize)
			}
		})
	}
}

// TestRollbackOffsetBounded 遍历一段连续的写前大小与多种已写字节数，确认返回值
// 始终落在 [0, max(beforeSize,0)] 内：有界、不与写入字节数或编码发生耦合。
func TestRollbackOffsetBounded(t *testing.T) {
	writes := []int64{-7, -1, 0, 1, 2, 3, 1 << 20, math.MaxInt64}
	for before := int64(-3); before <= 64; before++ {
		for _, written := range writes {
			got := rollbackOffset(before, written)
			upper := before
			if upper < 0 {
				upper = 0
			}
			if got < 0 || got > upper {
				t.Fatalf("rollbackOffset(%d, %d) = %d, want within [0, %d]",
					before, written, got, upper)
			}
		}
	}
}
