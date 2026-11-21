package proc

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 覆盖 timedOut/signaled 的四种组合，逐字段校验 Alive、状态词与摘要。
func TestDescribeProcessAllCombinations(t *testing.T) {
	started := time.Date(2026, time.November, 21, 10, 0, 0, 0, time.UTC)
	now := started.Add(1500 * time.Millisecond)

	tests := []struct {
		name        string
		id          string
		timedOut    bool
		signaled    bool
		wantAlive   bool
		wantWord    string
		wantSummary string
	}{
		{
			name: "neither flag means alive", id: "p1",
			timedOut: false, signaled: false, wantAlive: true,
			wantWord:    healthAlive,
			wantSummary: `进程"p1"：存活，已运行1500毫秒`,
		},
		{
			name: "timed out", id: "p2",
			timedOut: true, signaled: false, wantAlive: false,
			wantWord:    healthTimedOut,
			wantSummary: `进程"p2"：已超时，已运行1500毫秒`,
		},
		{
			name: "signaled", id: "p3",
			timedOut: false, signaled: true, wantAlive: false,
			wantWord:    healthSignaled,
			wantSummary: `进程"p3"：已被信号终止，已运行1500毫秒`,
		},
		{
			name: "both flags", id: "p4",
			timedOut: true, signaled: true, wantAlive: false,
			wantWord:    healthTimedAndSignal,
			wantSummary: `进程"p4"：已超时且被信号终止，已运行1500毫秒`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DescribeProcess(tt.id, started, now, tt.timedOut, tt.signaled)
			if got.ID != tt.id {
				t.Fatalf("ID = %q, want %q", got.ID, tt.id)
			}
			if got.Alive != tt.wantAlive {
				t.Fatalf("Alive = %v, want %v", got.Alive, tt.wantAlive)
			}
			if got.TimedOut != tt.timedOut || got.Signaled != tt.signaled {
				t.Fatalf("TimedOut/Signaled = %v/%v, want %v/%v", got.TimedOut, got.Signaled, tt.timedOut, tt.signaled)
			}
			if got.AgeMS != 1500 {
				t.Fatalf("AgeMS = %d, want 1500", got.AgeMS)
			}
			if got.Summary != tt.wantSummary {
				t.Fatalf("Summary = %q, want %q", got.Summary, tt.wantSummary)
			}
			if !strings.Contains(got.Summary, tt.wantWord) {
				t.Fatalf("Summary %q 不含状态词 %q", got.Summary, tt.wantWord)
			}
		})
	}
}

// 年龄边界：now 早于 started、相等、亚毫秒、恰好毫秒与大于零的时长。
func TestDescribeProcessAgeBoundaries(t *testing.T) {
	base := time.Date(2026, time.November, 21, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		delta time.Duration
		want  int64
	}{
		{"now before started clamps to zero", -time.Second, 0},
		{"one nanosecond before clamps to zero", -time.Nanosecond, 0},
		{"equal times yield zero", 0, 0},
		{"sub-millisecond truncates to zero", 999 * time.Microsecond, 0},
		{"exactly one millisecond", time.Millisecond, 1},
		{"one and a half milliseconds truncates", 1500 * time.Microsecond, 1},
		{"large duration", 72*time.Hour + 345*time.Millisecond, int64(72*3600*1000 + 345)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DescribeProcess("p", base, base.Add(tt.delta), false, false)
			if got.AgeMS != tt.want {
				t.Fatalf("delta=%v AgeMS = %d, want %d", tt.delta, got.AgeMS, tt.want)
			}
			wantSummary := `进程"p"：存活，已运行` + strconv.FormatInt(tt.want, 10) + `毫秒`
			if got.Summary != wantSummary {
				t.Fatalf("Summary = %q, want %q", got.Summary, wantSummary)
			}
		})
	}
}

// CJK 标识原样进入 ID 与 Summary，不做按字节切分或特殊编码处理。
func TestDescribeProcessCJKID(t *testing.T) {
	for _, id := range []string{"进程", "中文-标签", "任务🚀节点", "混用ABC"} {
		started := time.Unix(0, 0)
		got := DescribeProcess(id, started, started.Add(2*time.Second), false, false)
		if got.ID != id {
			t.Fatalf("ID = %q, want %q", got.ID, id)
		}
		if !strings.Contains(got.Summary, id) {
			t.Fatalf("Summary %q 未包含原样的 CJK 标识 %q", got.Summary, id)
		}
		if strings.ContainsRune(got.Summary, utf8Replacement) {
			t.Fatalf("Summary %q 含 U+FFFD，CJK 标识被破坏", got.Summary)
		}
	}
}

// utf8Replacement 用于在摘要中检测是否意外产生了替换字符。
const utf8Replacement = '\uFFFD'

// 摘要稳定：同一输入重复调用结果一致，且并发调用无共享可变状态（-race 验证）。
func TestDescribeProcessSummaryStable(t *testing.T) {
	started := time.Unix(100, 0)
	now := started.Add(3 * time.Second)
	want := DescribeProcess("稳定-id", started, now, true, false)

	for i := 0; i < 100; i++ {
		got := DescribeProcess("稳定-id", started, now, true, false)
		if got != want {
			t.Fatalf("第 %d 次调用 = %+v, want %+v", i, got, want)
		}
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if got := DescribeProcess("稳定-id", started, now, true, false); got != want {
					t.Errorf("并发调用 = %+v, want %+v", got, want)
				}
			}
		}()
	}
	wg.Wait()
}

// 纯函数不修改入参：started/now 在调用前后保持原样。
func TestDescribeProcessDoesNotMutateInputs(t *testing.T) {
	started := time.Date(2026, time.November, 21, 10, 0, 0, 123, time.UTC)
	now := started.Add(5 * time.Millisecond)
	wantStarted, wantNow := started, now

	_ = DescribeProcess("p", started, now, true, true)

	if !started.Equal(wantStarted) || started != wantStarted {
		t.Fatalf("started 被修改: %v, want %v", started, wantStarted)
	}
	if !now.Equal(wantNow) || now != wantNow {
		t.Fatalf("now 被修改: %v, want %v", now, wantNow)
	}
}

// HealthOfResult 覆盖退出码与 Truncated 的组合，校验派生的 TimedOut/Signaled/摘要附注。
func TestHealthOfResult(t *testing.T) {
	tests := []struct {
		name        string
		code        int
		truncated   bool
		wantTimed   bool
		wantSignal  bool
		wantAlive   bool
		wantSummary string
	}{
		{
			name: "clean exit", code: 0,
			wantSummary: `进程"result"：存活，已运行10毫秒，退出码=0`,
			wantAlive:   true,
		},
		{
			name: "ordinary failure", code: 1,
			wantSummary: `进程"result"：存活，已运行10毫秒，退出码=1`,
			wantAlive:   true,
		},
		{
			name: "timeout code 124", code: 124,
			wantTimed:   true,
			wantSummary: `进程"result"：已超时，已运行10毫秒，退出码=124`,
		},
		{
			name: "signal code -1", code: -1,
			wantSignal:  true,
			wantSummary: `进程"result"：已被信号终止，已运行10毫秒，退出码=-1`,
		},
		{
			name: "signal code -15", code: -15,
			wantSignal:  true,
			wantSummary: `进程"result"：已被信号终止，已运行10毫秒，退出码=-15`,
		},
		{
			name: "interrupted code 130 is ordinary failure here", code: 130,
			wantAlive:   true,
			wantSummary: `进程"result"：存活，已运行10毫秒，退出码=130`,
		},
		{
			name: "truncated clean exit adds note but stays alive", code: 0, truncated: true,
			wantAlive:   true,
			wantSummary: `进程"result"：存活，已运行10毫秒，退出码=0，输出已截断`,
		},
		{
			name: "truncated timeout", code: 124, truncated: true,
			wantTimed:   true,
			wantSummary: `进程"result"：已超时，已运行10毫秒，退出码=124，输出已截断`,
		},
		{
			name: "negative code never becomes timeout", code: -1, truncated: true,
			wantSignal:  true,
			wantSummary: `进程"result"：已被信号终止，已运行10毫秒，退出码=-1，输出已截断`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{ExitCode: tt.code, Truncated: tt.truncated, Duration: 10 * time.Millisecond}
			got := HealthOfResult(r, 10*time.Millisecond)
			if got.ID != resultHealthID {
				t.Fatalf("ID = %q, want %q", got.ID, resultHealthID)
			}
			if got.TimedOut != tt.wantTimed || got.Signaled != tt.wantSignal {
				t.Fatalf("TimedOut/Signaled = %v/%v, want %v/%v", got.TimedOut, got.Signaled, tt.wantTimed, tt.wantSignal)
			}
			if got.Alive != tt.wantAlive {
				t.Fatalf("Alive = %v, want %v", got.Alive, tt.wantAlive)
			}
			if got.AgeMS != 10 {
				t.Fatalf("AgeMS = %d, want 10", got.AgeMS)
			}
			if got.Summary != tt.wantSummary {
				t.Fatalf("Summary = %q, want %q", got.Summary, tt.wantSummary)
			}
		})
	}
}

// HealthOfResult 的年龄边界：d 的负值与亚毫秒归 0，正值取毫秒。
func TestHealthOfResultAgeBoundaries(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want int64
	}{
		{"negative clamps to zero", -time.Second, 0},
		{"zero stays zero", 0, 0},
		{"sub-millisecond truncates to zero", 500 * time.Microsecond, 0},
		{"positive milliseconds", 123 * time.Millisecond, 123},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HealthOfResult(Result{ExitCode: 0}, tt.d)
			if got.AgeMS != tt.want {
				t.Fatalf("d=%v AgeMS = %d, want %d", tt.d, got.AgeMS, tt.want)
			}
			if !strings.Contains(got.Summary, fmt.Sprintf("已运行%d毫秒", tt.want)) {
				t.Fatalf("Summary %q 不含预期的毫秒数 %d", got.Summary, tt.want)
			}
		})
	}
}

// 纯函数不修改入参：Result（含其字符串与 Duration）在调用前后逐字段相同。
func TestHealthOfResultDoesNotMutateInputs(t *testing.T) {
	r := Result{
		Stdout:    "输出",
		Stderr:    "错误",
		ExitCode:  124,
		Truncated: true,
		Duration:  7 * time.Millisecond,
	}
	want := r

	_ = HealthOfResult(r, 9*time.Millisecond)

	if !reflect.DeepEqual(r, want) {
		t.Fatalf("Result 被修改: %+v, want %+v", r, want)
	}
}

// HealthOfResult 确定性：同一输入重复与并发调用均得到相同快照。
func TestHealthOfResultIsDeterministic(t *testing.T) {
	r := Result{Stdout: "x", ExitCode: -1, Truncated: true, Duration: time.Second}
	d := 250 * time.Millisecond
	want := HealthOfResult(r, d)

	for i := 0; i < 100; i++ {
		if got := HealthOfResult(r, d); got != want {
			t.Fatalf("第 %d 次调用 = %+v, want %+v", i, got, want)
		}
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if got := HealthOfResult(r, d); got != want {
					t.Errorf("并发调用 = %+v, want %+v", got, want)
				}
			}
		}()
	}
	wg.Wait()
}
