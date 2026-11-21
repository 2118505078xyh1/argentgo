package mcp

import (
	"reflect"
	"sync"
	"testing"
)

// historySnap 用 Gen 作为唯一标识的快照，便于在顺序断言中定位记录。
func historySnap(gen uint64, healthy bool) HealthSnapshot {
	return HealthSnapshot{Healthy: healthy, ToolCount: int(gen), Gen: gen}
}

// TestHealthHistoryRecordRecentRing 表驱动验证 Record/Recent 的环形回绕语义：
// 未回绕取尾部、回绕后仅保留最近 capacity 条、容量为 1 的退化情形。
func TestHealthHistoryRecordRecentRing(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		records  []HealthSnapshot
		recentN  int
		wantGen  []uint64 // 期望的 Gen 序列，按时间从旧到新
	}{
		{
			name:     "未回绕且 n 大于条数返回全部",
			capacity: 5,
			records:  []HealthSnapshot{historySnap(1, true), historySnap(2, true), historySnap(3, true)},
			recentN:  10,
			wantGen:  []uint64{1, 2, 3},
		},
		{
			name:     "未回绕取尾部 n 条",
			capacity: 5,
			records:  []HealthSnapshot{historySnap(1, true), historySnap(2, true), historySnap(3, true)},
			recentN:  2,
			wantGen:  []uint64{2, 3},
		},
		{
			name:     "回绕后仅保留最近 capacity 条",
			capacity: 3,
			records: []HealthSnapshot{
				historySnap(1, true), historySnap(2, true), historySnap(3, true),
				historySnap(4, true), historySnap(5, true),
			},
			recentN: 10,
			wantGen: []uint64{3, 4, 5},
		},
		{
			name:     "回绕后 n 小于已记录条数",
			capacity: 3,
			records: []HealthSnapshot{
				historySnap(1, true), historySnap(2, true), historySnap(3, true),
				historySnap(4, true), historySnap(5, true),
			},
			recentN: 2,
			wantGen: []uint64{4, 5},
		},
		{
			name:     "容量为 1 仅保留最新一条",
			capacity: 1,
			records: []HealthSnapshot{
				historySnap(1, true), historySnap(2, false), historySnap(3, true),
			},
			recentN: 10,
			wantGen: []uint64{3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthHistory(tt.capacity)
			for _, s := range tt.records {
				h.Record(s)
			}
			got := h.Recent(tt.recentN)
			if len(got) != len(tt.wantGen) {
				t.Fatalf("Recent(%d) 条数 = %d, want %d", tt.recentN, len(got), len(tt.wantGen))
			}
			for i, g := range got {
				if g.Gen != tt.wantGen[i] {
					t.Errorf("Recent(%d)[%d].Gen = %d, want %d（应按旧到新）", tt.recentN, i, g.Gen, tt.wantGen[i])
				}
			}
			// Recent(1) 必须恰好是最新一条。
			if one := h.Recent(1); len(one) != 1 || one[0].Gen != tt.wantGen[len(tt.wantGen)-1] {
				t.Errorf("Recent(1) = %+v, want 最新 Gen=%d", one, tt.wantGen[len(tt.wantGen)-1])
			}
		})
	}
}

// TestHealthHistoryRecentBounds 覆盖 n 的边界：负数、零、等于条数、大于条数、
// 巨大值，以及空历史上的调用，验证不会越界 panic 且返回非 nil 空切片。
func TestHealthHistoryRecentBounds(t *testing.T) {
	filled := NewHealthHistory(4)
	filled.Record(historySnap(1, true))
	filled.Record(historySnap(2, false))
	var empty HealthHistory

	tests := []struct {
		name    string
		h       *HealthHistory
		n       int
		wantLen int
	}{
		{"负数 n 返回空", filled, -1, 0},
		{"零 n 返回空", filled, 0, 0},
		{"n 等于条数", filled, 2, 2},
		{"n 大于条数返回全部", filled, 100, 2},
		{"巨大 n 不越界", filled, int(1) << 30, 2},
		{"空历史返回空", &empty, 5, 0},
		{"空历史零值 n", &empty, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.h.Recent(tt.n)
			if got == nil {
				t.Error("Recent 应返回非 nil 切片")
			}
			if len(got) != tt.wantLen {
				t.Errorf("Recent(%d) 条数 = %d, want %d", tt.n, len(got), tt.wantLen)
			}
		})
	}
}

// TestHealthHistoryUnhealthyStreak 表驱动覆盖尾部连续不健康的各种形态：
// 空历史、全健康、尾部连续、全不健康、健康打断、中间重置，以及回绕后的连续性。
func TestHealthHistoryUnhealthyStreak(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		healthy  []bool // 按 Record 顺序给出的健康标志
		want     int
	}{
		{"空历史", 4, nil, 0},
		{"全健康", 4, []bool{true, true, true}, 0},
		{"尾部一条不健康", 4, []bool{true, true, false}, 1},
		{"尾部连续多条不健康", 4, []bool{true, false, false, false}, 3},
		{"全不健康", 4, []bool{false, false, false}, 3},
		{"末尾健康打断连续", 4, []bool{false, false, false, true}, 0},
		{"中间健康重置计数", 4, []bool{false, true, false, false}, 2},
		{"回绕后尾部连续", 3, []bool{false, false, true, false, false}, 2},
		{"回绕后全不健康被容量截断", 2, []bool{false, false, false, false}, 2},
		{"容量为 1 的不健康", 1, []bool{false}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthHistory(tt.capacity)
			for i, healthy := range tt.healthy {
				h.Record(HealthSnapshot{Healthy: healthy, Gen: uint64(i + 1)})
			}
			// 连续调用两次，验证读操作无副作用。
			for call := 0; call < 2; call++ {
				if got := h.UnhealthyStreak(); got != tt.want {
					t.Errorf("第 %d 次 UnhealthyStreak() = %d, want %d", call+1, got, tt.want)
				}
			}
		})
	}
}

// TestHealthHistoryCJKLastError 验证含 CJK 的 LastError 文本在 Record/Recent
// 往返与回绕淘汰后仍逐字节保留，不被截断或改写。
func TestHealthHistoryCJKLastError(t *testing.T) {
	errA := "连接超时：服务器「服务甲」无响应"
	errB := "认证失败：令牌已过期（用户：张三）"
	h := NewHealthHistory(3)
	h.Record(HealthSnapshot{Healthy: false, LastError: errA, Gen: 1})
	h.Record(HealthSnapshot{Healthy: true, ToolCount: 2, Gen: 2})
	h.Record(HealthSnapshot{Healthy: false, LastError: errB, Gen: 3})
	h.Record(HealthSnapshot{Healthy: false, LastError: errA, Gen: 4}) // 回绕淘汰 Gen=1

	want := []HealthSnapshot{
		{Healthy: true, ToolCount: 2, Gen: 2},
		{Healthy: false, LastError: errB, Gen: 3},
		{Healthy: false, LastError: errA, Gen: 4},
	}
	if got := h.Recent(3); !reflect.DeepEqual(got, want) {
		t.Errorf("Recent(3) = %+v, want %+v", got, want)
	}
	if got := h.UnhealthyStreak(); got != 2 {
		t.Errorf("UnhealthyStreak() = %d, want 2", got)
	}
}

// TestHealthHistoryIsolation 验证“不改入参”：Record 不修改传入快照，
// 调用方事后改写原变量不影响已存记录；Recent 返回副本，改写副本不污染内部。
func TestHealthHistoryIsolation(t *testing.T) {
	h := NewHealthHistory(4)
	s := HealthSnapshot{Healthy: false, ToolCount: 7, LastError: "原始错误", Gen: 9}
	before := s
	h.Record(s)

	if s != before {
		t.Fatalf("Record 修改了入参: got %+v, want %+v", s, before)
	}
	// 改写原变量，不影响已存入的快照。
	s.Healthy = true
	s.ToolCount = 999
	s.LastError = "被改写"
	got := h.Recent(1)
	if len(got) != 1 || got[0] != before {
		t.Fatalf("改写原变量污染了内部记录: Recent(1) = %+v, want %+v", got, before)
	}
	// 改写 Recent 返回的副本，不影响下一次读取。
	got[0].ToolCount = -1
	got[0].LastError = "污染"
	if next := h.Recent(1); len(next) != 1 || next[0] != before {
		t.Fatalf("改写返回切片污染了内部记录: Recent(1) = %+v, want %+v", next, before)
	}
}

// TestHealthHistoryCapacityDefaults 验证默认容量与零值可用：
// 构造参数为 0/负数时取默认 64；零值直接 Record 会惰性按默认容量分配；
// 写入超过默认容量后仅保留最近 64 条。
func TestHealthHistoryCapacityDefaults(t *testing.T) {
	for _, c := range []int{0, -1, -100} {
		h := NewHealthHistory(c)
		if h.capacity != defaultHealthHistoryCapacity || len(h.buf) != defaultHealthHistoryCapacity || cap(h.buf) != defaultHealthHistoryCapacity {
			t.Errorf("NewHealthHistory(%d) 容量 = %d (len %d/cap %d), want %d",
				c, h.capacity, len(h.buf), cap(h.buf), defaultHealthHistoryCapacity)
		}
	}
	if h := NewHealthHistory(3); h.capacity != 3 || len(h.buf) != 3 {
		t.Errorf("NewHealthHistory(3) 容量 = %d (len %d), want 3", h.capacity, len(h.buf))
	}

	var zero HealthHistory
	if got := zero.Recent(5); len(got) != 0 {
		t.Errorf("零值 Recent 条数 = %d, want 0", len(got))
	}
	if got := zero.UnhealthyStreak(); got != 0 {
		t.Errorf("零值 UnhealthyStreak = %d, want 0", got)
	}
	total := defaultHealthHistoryCapacity + 10
	for i := 1; i <= total; i++ {
		zero.Record(historySnap(uint64(i), i%2 == 0))
	}
	if zero.capacity != defaultHealthHistoryCapacity || len(zero.buf) != defaultHealthHistoryCapacity {
		t.Errorf("零值惰性容量 = %d (len %d), want %d", zero.capacity, len(zero.buf), defaultHealthHistoryCapacity)
	}
	recent := zero.Recent(total)
	if len(recent) != defaultHealthHistoryCapacity {
		t.Fatalf("零值写入 %d 条后 Recent 条数 = %d, want %d", total, len(recent), defaultHealthHistoryCapacity)
	}
	if got, want := recent[0].Gen, uint64(11); got != want {
		t.Errorf("保留的最旧记录 Gen = %d, want %d", got, want)
	}
	if got, want := recent[len(recent)-1].Gen, uint64(total); got != want {
		t.Errorf("保留的最新记录 Gen = %d, want %d", got, want)
	}
}

// TestHealthHistoryConcurrent 在多写入者（Record）与多读者（Recent/
// UnhealthyStreak）并存的场景下验证并发安全：无 panic、无数据竞争，
// 完成后缓冲恰好保留 capacity 条且连续两次 Recent 结果一致。配合 -race 可检出竞态。
func TestHealthHistoryConcurrent(t *testing.T) {
	const capacity = 16
	const writers = 8
	const perWriter = 200
	h := NewHealthHistory(capacity)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				h.Record(HealthSnapshot{
					Healthy:   i%3 != 0,
					ToolCount: w,
					LastError: "并发错误",
					Gen:       uint64(w*perWriter + i + 1),
				})
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = h.Recent(capacity)
				_ = h.UnhealthyStreak()
			}
		}()
	}
	wg.Wait()

	first := h.Recent(100)
	if len(first) != capacity {
		t.Fatalf("并发后 Recent 条数 = %d, want %d", len(first), capacity)
	}
	if second := h.Recent(100); !reflect.DeepEqual(first, second) {
		t.Error("并发结束后连续两次 Recent 结果不一致")
	}
	if s := h.UnhealthyStreak(); s < 0 || s > capacity {
		t.Errorf("并发后 UnhealthyStreak = %d, 超出 [0,%d]", s, capacity)
	}
}
