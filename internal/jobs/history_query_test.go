package jobs

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// execCommands 提取记录的 Command 序列，保留 nil 语义。
func execCommands(recs []ExecRecord) []string {
	if recs == nil {
		return nil
	}
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Command
	}
	return out
}

// execRec 用给定命令与开始时间构造一条成功记录（退出码 0、耗时 10）。
func execRec(command string, startedMS int64) ExecRecord {
	return ExecRecord{Command: command, StartedMS: startedMS, ExitCode: 0, DurationMS: 10}
}

// TestExecHistoryQueryTimeWindow 表驱动验证 Query 的闭区间时间窗过滤、
// 稳定顺序、无匹配/空历史返回 nil、非法区间与环形回绕。
func TestExecHistoryQueryTimeWindow(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		adds     []ExecRecord
		minMS    int64
		maxMS    int64
		want     []ExecRecord
	}{
		{
			name:     "空历史返回nil",
			capacity: 3,
			adds:     nil,
			minMS:    0,
			maxMS:    100,
			want:     nil,
		},
		{
			name:     "全窗口按加入顺序返回",
			capacity: 3,
			adds: []ExecRecord{
				execRec("c1", 10), execRec("c2", 20), execRec("c3", 30),
			},
			minMS: 0,
			maxMS: 100,
			want: []ExecRecord{
				execRec("c1", 10), execRec("c2", 20), execRec("c3", 30),
			},
		},
		{
			name:     "窄窗口只返回区间内记录",
			capacity: 5,
			adds: []ExecRecord{
				execRec("a", 5), execRec("b", 15), execRec("c", 25), execRec("d", 35),
			},
			minMS: 10,
			maxMS: 30,
			want: []ExecRecord{
				execRec("b", 15), execRec("c", 25),
			},
		},
		{
			name:     "边界闭区间包含端点",
			capacity: 5,
			adds: []ExecRecord{
				execRec("a", 10), execRec("b", 20), execRec("c", 30),
			},
			minMS: 10,
			maxMS: 30,
			want: []ExecRecord{
				execRec("a", 10), execRec("b", 20), execRec("c", 30),
			},
		},
		{
			name:     "无匹配返回nil",
			capacity: 3,
			adds: []ExecRecord{
				execRec("a", 10), execRec("b", 20),
			},
			minMS: 100,
			maxMS: 200,
			want:  nil,
		},
		{
			name:     "非法区间min大于max返回nil",
			capacity: 3,
			adds: []ExecRecord{
				execRec("a", 10), execRec("b", 20),
			},
			minMS: 30,
			maxMS: 10,
			want:  nil,
		},
		{
			name:     "负时间戳区间",
			capacity: 3,
			adds: []ExecRecord{
				execRec("a", -30), execRec("b", -10), execRec("c", 10),
			},
			minMS: -20,
			maxMS: 0,
			want: []ExecRecord{
				execRec("b", -10),
			},
		},
		{
			name:     "环形回绕后仅查到现存记录",
			capacity: 2,
			adds: []ExecRecord{
				execRec("old", 5), execRec("b", 15), execRec("c", 25),
			},
			minMS: 0,
			maxMS: 100,
			want: []ExecRecord{
				execRec("b", 15), execRec("c", 25),
			},
		},
		{
			name:     "回绕后时间窗排除部分现存记录",
			capacity: 3,
			adds: []ExecRecord{
				execRec("old", 1), execRec("a", 10), execRec("b", 20), execRec("c", 30),
			},
			minMS: 20,
			maxMS: 40,
			want: []ExecRecord{
				execRec("b", 20), execRec("c", 30),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewExecHistory(tc.capacity)
			for _, r := range tc.adds {
				h.Add(r)
			}
			got := h.Query(tc.minMS, tc.maxMS)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Query(%d,%d) = %v, want %v", tc.minMS, tc.maxMS, got, tc.want)
			}
		})
	}
}

// TestExecHistoryFailed 表驱动验证 Failed 对退出码非零记录的选择、
// 稳定顺序、CJK 命令保留与空结果返回 nil。
func TestExecHistoryFailed(t *testing.T) {
	tests := []struct {
		name string
		adds []ExecRecord
		want []ExecRecord
	}{
		{
			name: "空历史返回nil",
			adds: nil,
			want: nil,
		},
		{
			name: "全部成功返回nil",
			adds: []ExecRecord{
				execRec("a", 1), execRec("b", 2),
			},
			want: nil,
		},
		{
			name: "仅返回失败记录且保持顺序",
			adds: []ExecRecord{
				execRec("a", 1),
				{Command: "b", StartedMS: 2, ExitCode: 1, DurationMS: 20},
				execRec("c", 3),
				{Command: "d", StartedMS: 4, ExitCode: 2, DurationMS: 40},
			},
			want: []ExecRecord{
				{Command: "b", StartedMS: 2, ExitCode: 1, DurationMS: 20},
				{Command: "d", StartedMS: 4, ExitCode: 2, DurationMS: 40},
			},
		},
		{
			name: "全部失败",
			adds: []ExecRecord{
				{Command: "x", StartedMS: 1, ExitCode: 1, DurationMS: 5},
				{Command: "y", StartedMS: 2, ExitCode: 127, DurationMS: 6},
			},
			want: []ExecRecord{
				{Command: "x", StartedMS: 1, ExitCode: 1, DurationMS: 5},
				{Command: "y", StartedMS: 2, ExitCode: 127, DurationMS: 6},
			},
		},
		{
			name: "负退出码也算失败",
			adds: []ExecRecord{
				execRec("ok", 1),
				{Command: "signal", StartedMS: 2, ExitCode: -1, DurationMS: 7},
			},
			want: []ExecRecord{
				{Command: "signal", StartedMS: 2, ExitCode: -1, DurationMS: 7},
			},
		},
		{
			name: "CJK命令保留",
			adds: []ExecRecord{
				{Command: "编译-项目甲", StartedMS: 1, ExitCode: 1, DurationMS: 9},
				execRec("编译-项目乙", 2),
				{Command: "测试-北京", StartedMS: 3, ExitCode: 3, DurationMS: 11},
			},
			want: []ExecRecord{
				{Command: "编译-项目甲", StartedMS: 1, ExitCode: 1, DurationMS: 9},
				{Command: "测试-北京", StartedMS: 3, ExitCode: 3, DurationMS: 11},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewExecHistory(8)
			for _, r := range tc.adds {
				h.Add(r)
			}
			got := h.Failed()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Failed() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExecHistoryStats 表驱动验证 Stats 的总数、失败数与平均耗时（整数除法
// 向零截断），覆盖空历史、回绕与负耗时。
func TestExecHistoryStats(t *testing.T) {
	tests := []struct {
		name              string
		capacity          int
		adds              []ExecRecord
		wantTotal         int
		wantFailed        int
		wantAvgDurationMS int64
	}{
		{
			name: "空历史全零", capacity: 3, adds: nil,
			wantTotal: 0, wantFailed: 0, wantAvgDurationMS: 0,
		},
		{
			name:     "全部成功整除",
			capacity: 3,
			adds: []ExecRecord{
				{Command: "a", StartedMS: 1, ExitCode: 0, DurationMS: 100},
				{Command: "b", StartedMS: 2, ExitCode: 0, DurationMS: 200},
				{Command: "c", StartedMS: 3, ExitCode: 0, DurationMS: 300},
			},
			wantTotal: 3, wantFailed: 0, wantAvgDurationMS: 200,
		},
		{
			name:     "混合成功与失败平均值截断",
			capacity: 5,
			adds: []ExecRecord{
				{Command: "a", StartedMS: 1, ExitCode: 0, DurationMS: 100},
				{Command: "b", StartedMS: 2, ExitCode: 1, DurationMS: 201},
				{Command: "c", StartedMS: 3, ExitCode: 2, DurationMS: 50},
			},
			wantTotal: 3, wantFailed: 2, wantAvgDurationMS: 117,
		},
		{
			name:     "全部失败",
			capacity: 5,
			adds: []ExecRecord{
				{Command: "a", StartedMS: 1, ExitCode: -1, DurationMS: 10},
				{Command: "b", StartedMS: 2, ExitCode: 1, DurationMS: 20},
			},
			wantTotal: 2, wantFailed: 2, wantAvgDurationMS: 15,
		},
		{
			name:     "回绕后仅统计现存记录",
			capacity: 2,
			adds: []ExecRecord{
				{Command: "old", StartedMS: 1, ExitCode: 1, DurationMS: 999},
				{Command: "b", StartedMS: 2, ExitCode: 0, DurationMS: 10},
				{Command: "c", StartedMS: 3, ExitCode: 1, DurationMS: 30},
			},
			wantTotal: 2, wantFailed: 1, wantAvgDurationMS: 20,
		},
		{
			name:     "负耗时向零截断",
			capacity: 3,
			adds: []ExecRecord{
				{Command: "a", StartedMS: 1, ExitCode: 0, DurationMS: -1},
				{Command: "b", StartedMS: 2, ExitCode: 0, DurationMS: 2},
			},
			wantTotal: 2, wantFailed: 0, wantAvgDurationMS: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewExecHistory(tc.capacity)
			for _, r := range tc.adds {
				h.Add(r)
			}
			total, failed, avg := h.Stats()
			if total != tc.wantTotal || failed != tc.wantFailed || avg != tc.wantAvgDurationMS {
				t.Fatalf("Stats() = (%d,%d,%d), want (%d,%d,%d)",
					total, failed, avg, tc.wantTotal, tc.wantFailed, tc.wantAvgDurationMS)
			}
		})
	}
}

// TestExecHistoryRingWrap 表驱动验证不同容量（含钳制）下环形回绕后只保留
// 最近 capacity 条记录，并通过全窗口 Query 观察稳定顺序。
func TestExecHistoryRingWrap(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		adds     []ExecRecord
		want     []string
	}{
		{
			name:     "未写满保留全部",
			capacity: 3,
			adds:     []ExecRecord{execRec("a", 1), execRec("b", 2)},
			want:     []string{"a", "b"},
		},
		{
			name:     "恰好写满不丢弃",
			capacity: 3,
			adds:     []ExecRecord{execRec("a", 1), execRec("b", 2), execRec("c", 3)},
			want:     []string{"a", "b", "c"},
		},
		{
			name:     "回绕一项丢弃最旧",
			capacity: 3,
			adds: []ExecRecord{
				execRec("a", 1), execRec("b", 2), execRec("c", 3), execRec("d", 4),
			},
			want: []string{"b", "c", "d"},
		},
		{
			name:     "多次回绕只留最近capacity条",
			capacity: 3,
			adds: []ExecRecord{
				execRec("a", 1), execRec("b", 2), execRec("c", 3), execRec("d", 4),
				execRec("e", 5), execRec("f", 6), execRec("g", 7),
			},
			want: []string{"e", "f", "g"},
		},
		{
			name:     "容量1只留最新",
			capacity: 1,
			adds:     []ExecRecord{execRec("a", 1), execRec("b", 2), execRec("c", 3)},
			want:     []string{"c"},
		},
		{
			name:     "容量0钳制为1",
			capacity: 0,
			adds:     []ExecRecord{execRec("a", 1), execRec("b", 2)},
			want:     []string{"b"},
		},
		{
			name:     "负容量钳制为1",
			capacity: -5,
			adds:     []ExecRecord{execRec("a", 1), execRec("b", 2)},
			want:     []string{"b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewExecHistory(tc.capacity)
			for _, r := range tc.adds {
				h.Add(r)
			}
			got := execCommands(h.Query(0, 1<<60))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("回绕后 Query 命令 = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExecHistoryZeroValue 验证零值 ExecHistory 可直接调用全部方法：
// 初始查询返回零值，Add 后按容量 1 保留最新一条。
func TestExecHistoryZeroValue(t *testing.T) {
	var h ExecHistory

	if got := h.Query(0, 100); got != nil {
		t.Fatalf("零值 Query = %v, want nil", got)
	}
	if got := h.Failed(); got != nil {
		t.Fatalf("零值 Failed = %v, want nil", got)
	}
	if total, failed, avg := h.Stats(); total != 0 || failed != 0 || avg != 0 {
		t.Fatalf("零值 Stats = (%d,%d,%d), want (0,0,0)", total, failed, avg)
	}

	h.Add(execRec("first", 1))
	h.Add(execRec("second", 2))

	if got := execCommands(h.Query(0, 100)); !reflect.DeepEqual(got, []string{"second"}) {
		t.Fatalf("零值 Add 后 Query = %v, want [second]", got)
	}
	if total, _, _ := h.Stats(); total != 1 {
		t.Fatalf("零值 Add 后 Stats total = %d, want 1", total)
	}
}

// TestExecHistoryDoesNotMutateInput 验证 Add 按值保存：修改传入的记录后
// 历史不受影响；修改 Query/Failed 返回的切片后历史同样不受影响。
func TestExecHistoryDoesNotMutateInput(t *testing.T) {
	h := NewExecHistory(4)

	input := ExecRecord{Command: "原始", StartedMS: 10, ExitCode: 1, DurationMS: 100}
	h.Add(input)
	// 修改入参，历史应保留加入时的值。
	input.Command = "被修改"
	input.StartedMS = 999
	input.ExitCode = 0
	input.DurationMS = 999

	want := []ExecRecord{{Command: "原始", StartedMS: 10, ExitCode: 1, DurationMS: 100}}
	if got := h.Query(0, 1000); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改入参后 Query = %v, want %v", got, want)
	}
	if got := h.Failed(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改入参后 Failed = %v, want %v", got, want)
	}

	// 修改返回切片的元素，历史应保持不变。
	q := h.Query(0, 1000)
	q[0].Command = "污染查询"
	f := h.Failed()
	f[0].ExitCode = 0

	if got := h.Query(0, 1000); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改返回切片后 Query = %v, want %v", got, want)
	}
	if total, failed, avg := h.Stats(); total != 1 || failed != 1 || avg != 100 {
		t.Fatalf("修改返回切片后 Stats = (%d,%d,%d), want (1,1,100)", total, failed, avg)
	}
}

// TestExecHistoryConcurrent 在并发 Add/Query/Failed/Stats 下验证锁保护：
// 最终总数应等于实际写入数，且 -race 下无数据竞争。
func TestExecHistoryConcurrent(t *testing.T) {
	const (
		writers   = 8
		perWriter = 500
	)
	// 容量足够大以保留全部写入，便于断言最终总数。
	h := NewExecHistory(writers * perWriter)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				r := ExecRecord{
					Command:    fmt.Sprintf("cmd-%d-%d", w, i),
					StartedMS:  int64(w*perWriter + i),
					ExitCode:   i % 3,
					DurationMS: int64(i % 17),
				}
				h.Add(r)
			}
		}(w)
	}
	for r := 0; r < writers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = h.Query(0, 1<<60)
				_ = h.Failed()
				_, _, _ = h.Stats()
			}
		}()
	}
	wg.Wait()

	wantTotal := writers * perWriter
	if total, _, _ := h.Stats(); total != wantTotal {
		t.Fatalf("并发写入后 Stats total = %d, want %d", total, wantTotal)
	}
	if got := len(h.Query(0, 1<<60)); got != wantTotal {
		t.Fatalf("并发写入后 Query 长度 = %d, want %d", got, wantTotal)
	}
	// 退出码 i%3 中，i%3==0 为成功，其余失败：每个 writer 中失败数为 2/3。
	wantFailed := 0
	for i := 0; i < perWriter; i++ {
		if i%3 != 0 {
			wantFailed++
		}
	}
	wantFailed *= writers
	if _, failed, _ := h.Stats(); failed != wantFailed {
		t.Fatalf("并发写入后 Stats failed = %d, want %d", failed, wantFailed)
	}
}
