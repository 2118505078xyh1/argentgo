package proc

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 本文件为 R12 process_health 原语提供差分守护：所有期望值都由本文件中的
// 独立参考实现计算（使用字面状态词、手工毫秒换算与手工钳位），不调用任何
// 生产函数（DescribeProcess / HealthOfResult / healthSummary / healthStatusWord /
// newProcessHealth），从而在生产实现漂移时能真正暴露差异。

// refResultID 是参考实现中 HealthOfResult 使用的固定标签，刻意写成字面值，
// 避免复用生产常量导致守护失效。
const refResultID = "result"

// 参考实现：把纳秒时长换算为毫秒，向零截断。刻意使用字面除数 1_000_000，
// 不调用 time.Duration.Milliseconds，以免与被测实现共用同一换算路径。
func refAgeMS(d time.Duration) int64 {
	return int64(d) / 1_000_000
}

// 参考实现：timedOut/signaled 组合到稳定中文状态词的映射，使用字面字符串。
func refStatusWord(timedOut, signaled bool) string {
	switch {
	case timedOut && signaled:
		return "已超时且被信号终止"
	case timedOut:
		return "已超时"
	case signaled:
		return "已被信号终止"
	default:
		return "存活"
	}
}

// 参考实现：固定格式中文摘要，extra 非空时以全角逗号追加为附注。
func refSummary(id string, ageMS int64, timedOut, signaled bool, extra string) string {
	s := fmt.Sprintf("进程%q：%s，已运行%d毫秒", id, refStatusWord(timedOut, signaled), ageMS)
	if extra != "" {
		s += "，" + extra
	}
	return s
}

// 参考实现：健康快照核心，ageMS < 0 时钳到 0；其余字段由布尔组合推导。
func refHealth(id string, ageMS int64, timedOut, signaled bool, extra string) ProcessHealth {
	if ageMS < 0 {
		ageMS = 0
	}
	return ProcessHealth{
		ID:       id,
		Alive:    !timedOut && !signaled,
		AgeMS:    ageMS,
		TimedOut: timedOut,
		Signaled: signaled,
		Summary:  refSummary(id, ageMS, timedOut, signaled, extra),
	}
}

// 参考实现：DescribeProcess 的期望快照。
func refDescribeProcess(id string, started, now time.Time, timedOut, signaled bool) ProcessHealth {
	return refHealth(id, refAgeMS(now.Sub(started)), timedOut, signaled, "")
}

// 参考实现：HealthOfResult 的期望快照。ExitCode==124 视为超时，ExitCode<0
// 视为被信号终止；Truncated 只作为摘要附注，不改变三个布尔字段。
func refHealthOfResult(r Result, d time.Duration) ProcessHealth {
	timedOut := r.ExitCode == 124
	signaled := r.ExitCode < 0
	extra := "退出码=" + strconv.Itoa(r.ExitCode)
	if r.Truncated {
		extra += "，输出已截断"
	}
	return refHealth(refResultID, refAgeMS(d), timedOut, signaled, extra)
}

// 逐字段比对健康快照，任何一处不一致都立即定位到具体字段。
func assertHealthEqual(t *testing.T, got, want ProcessHealth) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Alive != want.Alive {
		t.Errorf("Alive = %v, want %v", got.Alive, want.Alive)
	}
	if got.AgeMS != want.AgeMS {
		t.Errorf("AgeMS = %d, want %d", got.AgeMS, want.AgeMS)
	}
	if got.TimedOut != want.TimedOut {
		t.Errorf("TimedOut = %v, want %v", got.TimedOut, want.TimedOut)
	}
	if got.Signaled != want.Signaled {
		t.Errorf("Signaled = %v, want %v", got.Signaled, want.Signaled)
	}
	if got.Summary != want.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, want.Summary)
	}
}

// 时间溢出：now-started 超过 time.Duration 表示范围时 Sub 饱和到 Min/MaxInt64。
// 这里用 year 1 与 year 9999 两个极值时间触发饱和，并断言饱和后的毫秒值。
func TestHealthGuardDescribeProcessTimeOverflow(t *testing.T) {
	epoch := time.Time{}                                                                      // 公元 1 年，零值
	future := time.Date(9999, 12, 31, 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC) // 公元 9999 年末

	// 前置条件：两侧 Sub 必须确实饱和，否则本测试失去意义。
	if got := future.Sub(epoch); got != time.Duration(math.MaxInt64) {
		t.Fatalf("future.Sub(epoch) = %v, want MaxInt64", got)
	}
	if got := epoch.Sub(future); got != time.Duration(math.MinInt64) {
		t.Fatalf("epoch.Sub(future) = %v, want MinInt64", got)
	}

	const (
		wantMaxMS = int64(9223372036854) // math.MaxInt64 / 1e6，向零截断
		wantMinMS = int64(0)             // math.MinInt64 / 1e6 为负，钳到 0
	)

	tests := []struct {
		name               string
		started, now       time.Time
		timedOut, signaled bool
		wantAgeMS          int64
	}{
		{"positive saturation", epoch, future, false, false, wantMaxMS},
		{"positive saturation with both flags", epoch, future, true, true, wantMaxMS},
		{"negative saturation clamps", future, epoch, false, false, wantMinMS},
		{"negative saturation with signal", future, epoch, false, true, wantMinMS},
		{"negative saturation with timeout", future, epoch, true, false, wantMinMS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DescribeProcess("溢出-id", tt.started, tt.now, tt.timedOut, tt.signaled)
			want := refDescribeProcess("溢出-id", tt.started, tt.now, tt.timedOut, tt.signaled)
			assertHealthEqual(t, got, want)
			if got.AgeMS != tt.wantAgeMS {
				t.Fatalf("AgeMS = %d, want %d", got.AgeMS, tt.wantAgeMS)
			}
		})
	}
}

// 时间溢出：HealthOfResult 直接接收 time.Duration，可精确构造 Min/MaxInt64
// 及其临界邻近值，验证毫秒换算与负数钳位。
func TestHealthGuardHealthOfResultTimeOverflow(t *testing.T) {
	const (
		wantMaxMS = int64(9223372036854) // math.MaxInt64 / 1e6
	)

	tests := []struct {
		name      string
		d         time.Duration
		wantAgeMS int64
	}{
		{"MaxInt64", time.Duration(math.MaxInt64), wantMaxMS},
		{"MaxInt64-1", time.Duration(math.MaxInt64 - 1), wantMaxMS},
		{"max exact millisecond boundary", time.Duration(9_223_372_036_854_000_000), wantMaxMS},
		{"MinInt64", time.Duration(math.MinInt64), 0},
		{"MinInt64+1", time.Duration(math.MinInt64 + 1), 0},
		{"min exact millisecond boundary", time.Duration(-9_223_372_036_854_000_000), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{Stdout: "敏感", Stderr: "敏感", ExitCode: 124, Truncated: true, Duration: tt.d}
			got := HealthOfResult(r, tt.d)
			want := refHealthOfResult(r, tt.d)
			assertHealthEqual(t, got, want)
			if got.AgeMS != tt.wantAgeMS {
				t.Fatalf("d=%v AgeMS = %d, want %d", tt.d, got.AgeMS, tt.wantAgeMS)
			}
		})
	}
}

// 亚毫秒截断：不足 1ms 的时长一律归 0，1ms 及以上向零截断到整毫秒。
// 两个生产函数都参与守护，确保它们对同一时长的截断口径一致。
func TestHealthGuardSubMillisecondTruncation(t *testing.T) {
	started := time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		d         time.Duration
		wantAgeMS int64
	}{
		{"one nanosecond", time.Nanosecond, 0},
		{"one microsecond", time.Microsecond, 0},
		{"999 microseconds", 999 * time.Microsecond, 0},
		{"999999 nanoseconds", 999999 * time.Nanosecond, 0},
		{"just below one millisecond", time.Millisecond - time.Nanosecond, 0},
		{"exactly one millisecond", time.Millisecond, 1},
		{"one millisecond plus nanosecond", time.Millisecond + time.Nanosecond, 1},
		{"one and a half milliseconds", 1500 * time.Microsecond, 1},
	}
	for _, tt := range tests {
		t.Run("DescribeProcess/"+tt.name, func(t *testing.T) {
			now := started.Add(tt.d)
			got := DescribeProcess("p", started, now, false, false)
			want := refDescribeProcess("p", started, now, false, false)
			assertHealthEqual(t, got, want)
			if got.AgeMS != tt.wantAgeMS {
				t.Fatalf("d=%v AgeMS = %d, want %d", tt.d, got.AgeMS, tt.wantAgeMS)
			}
		})
		t.Run("HealthOfResult/"+tt.name, func(t *testing.T) {
			r := Result{ExitCode: 0}
			got := HealthOfResult(r, tt.d)
			want := refHealthOfResult(r, tt.d)
			assertHealthEqual(t, got, want)
			if got.AgeMS != tt.wantAgeMS {
				t.Fatalf("d=%v AgeMS = %d, want %d", tt.d, got.AgeMS, tt.wantAgeMS)
			}
		})
	}
}

// 退出码全谱 × Truncated 组合：对每个组合逐字段与独立参考实现比对，
// 并独立推导 TimedOut/Signaled/Alive 与摘要附注，防止两个实现同时犯同一错误。
func TestHealthGuardExitCodeSpectrum(t *testing.T) {
	codes := []int{
		-1, -9, -15, math.MinInt32, // 负数：被信号终止
		0, 1, // 正常与通用失败
		124, 125, 126, 127, // GNU timeout 及其邻近码
		128, 130, 137, 143, // 128+N：SIGINT / SIGKILL / SIGTERM 等
		255, 1000, math.MaxInt32, 1 << 40, math.MaxInt, // 大数
	}
	for _, code := range codes {
		for _, truncated := range []bool{false, true} {
			name := fmt.Sprintf("code=%d/truncated=%v", code, truncated)
			t.Run(name, func(t *testing.T) {
				d := 250 * time.Millisecond
				r := Result{
					Stdout:    "SECRET_STDOUT_敏感数据",
					Stderr:    "SECRET_STDERR_敏感数据",
					ExitCode:  code,
					Truncated: truncated,
					Duration:  d,
				}
				got := HealthOfResult(r, d)
				want := refHealthOfResult(r, d)
				assertHealthEqual(t, got, want)

				// 独立推导三个布尔字段
				wantTimed := code == 124
				wantSignal := code < 0
				wantAlive := !wantTimed && !wantSignal
				if got.TimedOut != wantTimed || got.Signaled != wantSignal || got.Alive != wantAlive {
					t.Fatalf("code=%d: Alive/TimedOut/Signaled = %v/%v/%v, want %v/%v/%v",
						code, got.Alive, got.TimedOut, got.Signaled, wantAlive, wantTimed, wantSignal)
				}

				// 摘要必须携带退出码，且截断附注与 Truncated 严格对应
				if !strings.Contains(got.Summary, "退出码="+strconv.Itoa(code)) {
					t.Errorf("Summary %q 不含退出码 %d", got.Summary, code)
				}
				if has := strings.Contains(got.Summary, "输出已截断"); has != truncated {
					t.Errorf("Summary %q 的截断附注=%v, want %v", got.Summary, has, truncated)
				}
			})
		}
	}
}

// 摘要不得泄漏 Result 中的原始敏感字段（Stdout/Stderr），也不得夹带换行。
func TestHealthGuardSummaryExcludesSensitiveFields(t *testing.T) {
	r := Result{
		Stdout:    "TOPSECRET_stdout_绝不出现在摘要",
		Stderr:    "TOPSECRET_stderr_绝不出现在摘要",
		ExitCode:  124,
		Truncated: true,
		Duration:  3 * time.Second,
	}
	got := HealthOfResult(r, 3*time.Second)
	for _, secret := range []string{r.Stdout, r.Stderr} {
		if strings.Contains(got.Summary, secret) {
			t.Errorf("Summary %q 泄漏敏感字段 %q", got.Summary, secret)
		}
	}
	if strings.ContainsAny(got.Summary, "\n\r") {
		t.Errorf("Summary %q 含换行符", got.Summary)
	}
	// 摘要应仅由状态词、毫秒、退出码与截断附注组成
	wantSummary := refSummary(refResultID, 3000, true, false, "退出码=124，输出已截断")
	if got.Summary != wantSummary {
		t.Fatalf("Summary = %q, want %q", got.Summary, wantSummary)
	}
}

// CJK 标识必须逐字节保留：ID 与字节级相等，Summary 包含其原始字节，且不含 U+FFFD。
func TestHealthGuardCJKIDByteExact(t *testing.T) {
	ids := []string{
		"进程",      // 中文（3 字节/字）
		"任务🚀节点",   // 含 4 字节 emoji
		"日本語テスト",  // 日文汉字 + 假名
		"정상现代汉语",  // 韩文 + 中文混排
		"中文ABC混合", // 多字节与 ASCII 混排
	}
	started := time.Unix(0, 0)
	now := started.Add(1234 * time.Millisecond)
	replacement := []byte{0xEF, 0xBF, 0xBD} // U+FFFD 的 UTF-8 编码
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			got := DescribeProcess(id, started, now, false, true)
			want := refDescribeProcess(id, started, now, false, true)
			assertHealthEqual(t, got, want)

			if !bytes.Equal([]byte(got.ID), []byte(id)) {
				t.Fatalf("ID 字节不一致: got %x, want %x", []byte(got.ID), []byte(id))
			}
			if !bytes.Contains([]byte(got.Summary), []byte(id)) {
				t.Fatalf("Summary %q 未逐字节包含标识 %q", got.Summary, id)
			}
			if bytes.Contains([]byte(got.Summary), replacement) {
				t.Fatalf("Summary %q 含 U+FFFD，CJK 标识被破坏", got.Summary)
			}
		})
	}
}

// 确定性：同一输入重复 100 次，两个生产函数都必须返回完全相同的快照。
func TestHealthGuardDeterminism100(t *testing.T) {
	started := time.Date(2026, time.November, 21, 10, 0, 0, 0, time.UTC)
	now := started.Add(77 * time.Millisecond)
	wantD := DescribeProcess("稳定-id", started, now, true, true)
	for i := 0; i < 100; i++ {
		if got := DescribeProcess("稳定-id", started, now, true, true); got != wantD {
			t.Fatalf("DescribeProcess 第 %d 次调用 = %+v, want %+v", i, got, wantD)
		}
	}

	r := Result{Stdout: "x", Stderr: "y", ExitCode: 124, Truncated: true, Duration: 5 * time.Millisecond}
	d := 5 * time.Millisecond
	wantR := HealthOfResult(r, d)
	for i := 0; i < 100; i++ {
		if got := HealthOfResult(r, d); got != wantR {
			t.Fatalf("HealthOfResult 第 %d 次调用 = %+v, want %+v", i, got, wantR)
		}
	}
}

// 纯函数：两个构造函数都不得修改各自入参。
func TestHealthGuardDoesNotMutateInputs(t *testing.T) {
	started := time.Date(2026, time.November, 21, 10, 0, 0, 123, time.UTC)
	now := started.Add(5 * time.Millisecond)
	startedBefore, nowBefore := started, now
	_ = DescribeProcess("p", started, now, true, true)
	if started != startedBefore || now != nowBefore {
		t.Fatalf("DescribeProcess 修改了时间入参: started=%v now=%v", started, now)
	}

	r := Result{Stdout: "输出", Stderr: "错误", ExitCode: 124, Truncated: true, Duration: 7 * time.Millisecond}
	rBefore := r
	_ = HealthOfResult(r, 9*time.Millisecond)
	if !reflect.DeepEqual(r, rBefore) {
		t.Fatalf("HealthOfResult 修改了 Result: got %+v, want %+v", r, rBefore)
	}
}

// 交叉一致性：同一事实经两个生产原语表达时，Alive/TimedOut/Signaled/AgeMS 必须一致。
func TestHealthGuardCrossPrimitiveConsistency(t *testing.T) {
	started := time.Unix(0, 0)
	d := 42 * time.Millisecond
	now := started.Add(d)
	for _, code := range []int{-1, 0, 124, 130} {
		r := Result{ExitCode: code}
		h1 := HealthOfResult(r, d)
		h2 := DescribeProcess(refResultID, started, now, h1.TimedOut, h1.Signaled)
		if h1.Alive != h2.Alive || h1.TimedOut != h2.TimedOut || h1.Signaled != h2.Signaled || h1.AgeMS != h2.AgeMS {
			t.Errorf("code=%d: HealthOfResult=%+v 与 DescribeProcess=%+v 不一致", code, h1, h2)
		}
	}
}
