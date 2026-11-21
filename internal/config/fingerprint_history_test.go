package config

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// buildFPHistory 按给定顺序把指纹记入指定容量的历史，便于表驱动用例构造状态。
func buildFPHistory(capacity int, fps ...string) *FingerprintHistory {
	h := NewFingerprintHistory(capacity)
	for _, fp := range fps {
		h.Record(fp)
	}
	return h
}

func TestFingerprintHistoryRecordRingAndDedup(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		records  []string
		want     []string // 期望 Recent(足够大) 的结果，按从旧到新
	}{
		{"顺序追加", 5, []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"相邻去重", 5, []string{"a", "a", "b", "b", "b", "c"}, []string{"a", "b", "c"}},
		{"空指纹被忽略且不影响去重", 5, []string{"a", "", "b", "", "b"}, []string{"a", "b"}},
		{"写满后覆盖最旧", 3, []string{"a", "b", "c", "d"}, []string{"b", "c", "d"}},
		{"环形回绕后相邻去重", 3, []string{"a", "b", "c", "a", "a"}, []string{"b", "c", "a"}},
		{"非相邻重复允许存在", 3, []string{"a", "b", "a"}, []string{"a", "b", "a"}},
		{"容量为一只留最新", 1, []string{"a", "a", "b"}, []string{"b"}},
		{"容量为零不记录", 0, []string{"a", "b"}, []string{}},
		{"负容量按零处理", -3, []string{"a", "b"}, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := buildFPHistory(tt.capacity, tt.records...)
			got := h.Recent(tt.capacity + len(tt.records) + 10)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Recent = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFingerprintHistoryRecent(t *testing.T) {
	linear := buildFPHistory(5, "a", "b", "c")
	wrapped := buildFPHistory(3, "a", "b", "c", "d", "e") // 内部最新三条为 c,d,e
	tests := []struct {
		name string
		h    *FingerprintHistory
		n    int
		want []string
	}{
		{"n 为零", linear, 0, []string{}},
		{"n 为负", linear, -1, []string{}},
		{"n 等于一", linear, 1, []string{"c"}},
		{"n 等于二", linear, 2, []string{"b", "c"}},
		{"n 等于条目数", linear, 3, []string{"a", "b", "c"}},
		{"n 超过条目数", linear, 100, []string{"a", "b", "c"}},
		{"空历史", NewFingerprintHistory(3), 5, []string{}},
		{"容量为", NewFingerprintHistory(0), 5, []string{}},
		{"回绕后取全部", wrapped, 10, []string{"c", "d", "e"}},
		{"回绕后取两条", wrapped, 2, []string{"d", "e"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.h.Recent(tt.n); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Recent(%d) = %v, want %v", tt.n, got, tt.want)
			}
		})
	}
}

func TestFingerprintHistoryChangedInLastN(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		records  []string
		n        int
		want     bool
	}{
		{"空历史", 5, nil, 5, false},
		{"单条记录", 5, []string{"a"}, 5, false},
		{"仅一条且窗口为一", 5, []string{"a"}, 1, false},
		{"窗口为零", 5, []string{"a", "b"}, 0, false},
		{"窗口为负", 5, []string{"a", "b"}, -1, false},
		{"窗口一只见最新", 5, []string{"a", "b"}, 1, false},
		{"两条不同", 5, []string{"a", "b"}, 2, true},
		{"三条取最后两条", 5, []string{"a", "b", "c"}, 2, true},
		{"相邻去重后仅一条", 5, []string{"a", "a", "a"}, 5, false},
		{"非相邻重复仍算变化", 5, []string{"a", "b", "a"}, 3, true},
		{"非相邻重复取两条", 5, []string{"a", "b", "a"}, 2, true},
		{"窗口超过历史", 5, []string{"a", "b"}, 100, true},
		{"容量为一永不变化", 1, []string{"a", "b", "c"}, 100, false},
		{"容量为零", 0, []string{"a", "b"}, 100, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := buildFPHistory(tt.capacity, tt.records...)
			if got := h.ChangedInLastN(tt.n); got != tt.want {
				t.Fatalf("ChangedInLastN(%d) = %v, want %v (Recent=%v)", tt.n, got, tt.want, h.Recent(tt.n))
			}
		})
	}
}

func TestFingerprintHistoryCJK(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		records  []string
		want     []string
	}{
		{"CJK 相邻去重", 4, []string{"模型一", "模型一", "模型二", "模型二", "模型三"}, []string{"模型一", "模型二", "模型三"}},
		{"CJK 环形回绕", 3, []string{"配置甲", "配置乙", "配置丙", "配置丁"}, []string{"配置乙", "配置丙", "配置丁"}},
		{"CJK 空串忽略", 4, []string{"模型一", "", "模型二"}, []string{"模型一", "模型二"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := buildFPHistory(tt.capacity, tt.records...)
			if got := h.Recent(100); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Recent = %v, want %v", got, tt.want)
			}
		})
	}

	if h := buildFPHistory(3, "模型一", "模型二"); !h.ChangedInLastN(2) {
		t.Error("CJK 指纹变化未被 ChangedInLastN 检出")
	}
	if h := buildFPHistory(3, "模型一", "模型一"); h.ChangedInLastN(5) {
		t.Error("CJK 相邻重复被误判为变化")
	}
}

func TestFingerprintHistoryZeroValue(t *testing.T) {
	var h FingerprintHistory
	h.Record("a")
	h.Record("") // 空指纹同样不影响零值状态
	if got := h.Recent(10); len(got) != 0 {
		t.Fatalf("零值 Recent = %v, want 空", got)
	}
	if h.ChangedInLastN(10) {
		t.Fatal("零值 ChangedInLastN 应为 false")
	}
}

func TestFingerprintHistoryRecentReturnsCopy(t *testing.T) {
	h := buildFPHistory(3, "a", "b", "c")
	out := h.Recent(3)
	for i := range out {
		out[i] = "篡改"
	}
	if got, want := h.Recent(3), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("修改 Recent 返回值污染了内部缓冲: got %v, want %v", got, want)
	}
	// 后续 Record 也不得与之前返回的切片共享底层数组。
	out = h.Recent(3)
	h.Record("d")
	if got, want := out, []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("新的写入改写了旧副本: got %v, want %v", got, want)
	}
}

func TestFingerprintHistoryConcurrent(t *testing.T) {
	const (
		capacity    = 128
		writers     = 8
		perWriter   = 100
		expectedCap = writers * perWriter
	)
	h := NewFingerprintHistory(capacity)

	var wg sync.WaitGroup
	// 读 goroutine 与写 goroutine 并行，覆盖读写锁路径。
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = h.Recent(capacity)
				_ = h.ChangedInLastN(capacity)
			}
		}()
	}
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if i%10 == 0 {
					h.Record("") // 空指纹并发写入应被安全忽略
				}
				h.Record(fmt.Sprintf("fp-%d-%d", id, i))
			}
		}(w)
	}
	wg.Wait()

	recent := h.Recent(capacity + expectedCap)
	if len(recent) > capacity {
		t.Fatalf("历史条目数 %d 超过容量 %d", len(recent), capacity)
	}
	for i, fp := range recent {
		if !strings.HasPrefix(fp, "fp-") {
			t.Errorf("第 %d 条不是预期指纹: %q", i, fp)
		}
		if i > 0 && fp == recent[i-1] {
			t.Errorf("出现相邻重复指纹: 索引 %d 与 %d 均为 %q", i-1, i, fp)
		}
	}
	// 写结束后重复读取应得到一致结果。
	if again := h.Recent(capacity + expectedCap); !reflect.DeepEqual(again, recent) {
		t.Fatal("并发写结束后 Recent 结果不稳定")
	}
}
