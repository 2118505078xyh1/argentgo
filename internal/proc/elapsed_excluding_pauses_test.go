package proc

import (
	"reflect"
	"testing"
	"time"
)

func TestElapsedExcludingPauses(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	// 默认窗口 [base, base+10s]，总耗时 10s。
	start, now := at(0), at(10)

	tests := []struct {
		name   string
		start  time.Time
		now    time.Time
		pauses [][2]time.Time
		want   time.Duration
	}{
		{
			name:  "无暂停等于总耗时",
			start: start, now: now,
			pauses: nil,
			want:   10 * time.Second,
		},
		{
			name:  "空暂停切片等于总耗时",
			start: start, now: now,
			pauses: [][2]time.Time{},
			want:   10 * time.Second,
		},
		{
			name:  "单个暂停",
			start: start, now: now,
			pauses: [][2]time.Time{{at(2), at(5)}},
			want:   7 * time.Second,
		},
		{
			name:  "多个不重叠暂停累加扣除",
			start: start, now: now,
			pauses: [][2]time.Time{{at(1), at(3)}, {at(6), at(8)}},
			want:   6 * time.Second,
		},
		{
			name:  "重叠暂停只按并集扣除一次",
			start: start, now: now,
			pauses: [][2]time.Time{{at(1), at(5)}, {at(3), at(7)}},
			want:   4 * time.Second,
		},
		{
			name:  "一个暂停完全包含另一个",
			start: start, now: now,
			pauses: [][2]time.Time{{at(2), at(4)}, {at(1), at(9)}},
			want:   2 * time.Second,
		},
		{
			name:  "首尾相接的暂停按并集合并",
			start: start, now: now,
			pauses: [][2]time.Time{{at(1), at(3)}, {at(3), at(5)}},
			want:   6 * time.Second,
		},
		{
			name:  "起点早于窗口被裁到窗口起点",
			start: start, now: now,
			pauses: [][2]time.Time{{at(-3), at(2)}},
			want:   8 * time.Second,
		},
		{
			name:  "终点晚于窗口被裁到窗口终点",
			start: start, now: now,
			pauses: [][2]time.Time{{at(6), at(20)}},
			want:   6 * time.Second,
		},
		{
			name:  "暂停完全覆盖窗口",
			start: start, now: now,
			pauses: [][2]time.Time{{at(-5), at(20)}},
			want:   0,
		},
		{
			name:  "暂停完全在窗口之前不影响结果",
			start: start, now: now,
			pauses: [][2]time.Time{{at(-10), at(-1)}},
			want:   10 * time.Second,
		},
		{
			name:  "暂停完全在窗口之后不影响结果",
			start: start, now: now,
			pauses: [][2]time.Time{{at(11), at(20)}},
			want:   10 * time.Second,
		},
		{
			name:  "未闭合暂停终点为零值视为延续到 now",
			start: start, now: now,
			pauses: [][2]time.Time{{at(3), {}}},
			want:   3 * time.Second,
		},
		{
			name:  "未闭合暂停终点早于起点视为延续到 now",
			start: start, now: now,
			pauses: [][2]time.Time{{at(8), at(3)}},
			want:   8 * time.Second,
		},
		{
			name:  "没有起点的暂停被忽略",
			start: start, now: now,
			pauses: [][2]time.Time{{time.Time{}, at(5)}},
			want:   10 * time.Second,
		},
		{
			name:  "未闭合暂停与正常暂停并存",
			start: start, now: now,
			pauses: [][2]time.Time{{at(1), at(3)}, {at(6), {}}},
			want:   4 * time.Second,
		},
		{
			name:  "时间倒流返回零",
			start: start, now: at(-1),
			pauses: nil,
			want:   0,
		},
		{
			name:  "起止重合返回零",
			start: start, now: start,
			pauses: [][2]time.Time{{at(1), at(2)}},
			want:   0,
		},
		{
			name:  "暂停恰好落在窗口起点",
			start: start, now: now,
			pauses: [][2]time.Time{{at(0), at(2)}},
			want:   8 * time.Second,
		},
		{
			name:  "暂停恰好落在窗口终点",
			start: start, now: now,
			pauses: [][2]time.Time{{at(8), at(10)}},
			want:   8 * time.Second,
		},
		{
			name:  "零长度暂停不改变结果",
			start: start, now: now,
			pauses: [][2]time.Time{{at(5), at(5)}},
			want:   10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := elapsedExcludingPauses(tt.start, tt.now, tt.pauses); got != tt.want {
				t.Errorf("elapsedExcludingPauses() = %s，期望 %s", got, tt.want)
			}
		})
	}
}

func TestElapsedExcludingPausesDoesNotMutateInput(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	start, now := base, base.Add(10*time.Second)
	// 故意打乱顺序且带有重叠/越界，确保排序与裁剪只在副本上进行。
	pauses := [][2]time.Time{
		{base.Add(6 * time.Second), base.Add(20 * time.Second)},
		{base.Add(1 * time.Second), base.Add(5 * time.Second)},
		{base.Add(3 * time.Second), base.Add(7 * time.Second)},
	}
	original := make([][2]time.Time, len(pauses))
	copy(original, pauses)

	_ = elapsedExcludingPauses(start, now, pauses)

	if !reflect.DeepEqual(pauses, original) {
		t.Errorf("入参被修改：得到 %v，期望 %v", pauses, original)
	}
}

func TestElapsedExcludingPausesMonotonicClock(t *testing.T) {
	// 用 time.Now() 及其 Add 构造带单调时钟读数的时刻，
	// 走 Sub/Before/After 的单调分支，确认墙上时钟校正不影响结果。
	start := time.Now()
	now := start.Add(10 * time.Second)
	pauses := [][2]time.Time{
		{start.Add(2 * time.Second), start.Add(5 * time.Second)},
		{start.Add(4 * time.Second), start.Add(6 * time.Second)},
	}

	// 暂停并集为 [2s, 6s] 共 4s，有效耗时应为 6s。
	if got, want := elapsedExcludingPauses(start, now, pauses), 6*time.Second; got != want {
		t.Errorf("单调时钟路径：得到 %s，期望 %s", got, want)
	}
}
