package proc

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestClassifyExit 覆盖六种分类、首因优先级、边界退出码与 CJK 信号串。
func TestClassifyExit(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		signal   string
		timedOut bool
		spawnErr bool
		aborted  bool
		want     ExitClass
	}{
		// 每种分类的基础情形。
		{"clean exit", 0, "", false, false, false, ExitSuccess},
		{"generic failure", 1, "", false, false, false, ExitFailed},
		{"negative code is failure", -1, "", false, false, false, ExitFailed},
		{"large code is failure", 130, "", false, false, false, ExitFailed},
		{"timeout flag", 42, "", true, false, false, ExitTimeout},
		{"signal string", 9, "SIGKILL", false, false, false, ExitSignaled},
		{"spawn error flag", -1, "", false, true, false, ExitSpawnErr},
		{"aborted flag", 0, "", false, false, true, ExitAborted},

		// 首因优先级：spawn-error > aborted > timeout > signaled > success > failed。
		{"spawn wins over abort", 0, "", false, true, true, ExitSpawnErr},
		{"spawn wins over timeout", 0, "", true, true, false, ExitSpawnErr},
		{"spawn wins over signal", 0, "SIGTERM", false, true, false, ExitSpawnErr},
		{"spawn wins over failure", 7, "", false, true, false, ExitSpawnErr},
		{"abort wins over timeout", 0, "", true, false, true, ExitAborted},
		{"abort wins over signal", 0, "SIGINT", false, false, true, ExitAborted},
		{"abort wins over success", 0, "", false, false, true, ExitAborted},
		{"abort wins over failure", 1, "", false, false, true, ExitAborted},
		{"timeout wins over signal", 0, "SIGTERM", true, false, false, ExitTimeout},
		{"timeout wins over success", 0, "", true, false, false, ExitTimeout},
		{"timeout wins over failure", 1, "", true, false, false, ExitTimeout},
		{"signal wins over success", 0, "SIGTERM", false, false, false, ExitSignaled},
		{"signal wins over failure", 7, "SIGKILL", false, false, false, ExitSignaled},
		{"success wins over nothing else", 0, "", false, false, false, ExitSuccess},

		// 全部异常标记同时出现时，仍由最高优先级的 spawn-error 决定。
		{"all flags pick spawn", 7, "SIGKILL", true, true, true, ExitSpawnErr},
		// 除 spawn 外全部出现时由 aborted 决定。
		{"all but spawn pick abort", 7, "SIGKILL", true, false, true, ExitAborted},

		// CJK 信号：只判空，不做按字节切分，中文同样触发 signaled。
		{"single cjk signal", 9, "中断", false, false, false, ExitSignaled},
		{"multi cjk signal with zero code", 0, "信号：用户已取消", false, false, false, ExitSignaled},
		{"cjk signal still below timeout", 0, "信号中断", true, false, false, ExitTimeout},
		{"cjk signal still below abort", 0, "信号中断", false, false, true, ExitAborted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyExit(tt.exitCode, tt.signal, tt.timedOut, tt.spawnErr, tt.aborted)
			if got != tt.want {
				t.Fatalf("ClassifyExit(%d, %q, %v, %v, %v) = %q, want %q",
					tt.exitCode, tt.signal, tt.timedOut, tt.spawnErr, tt.aborted, got, tt.want)
			}
		})
	}
}

// TestClassifyExitLabelsAreStable 保证返回值始终落在六个稳定字面值之内，
// 且相同输入重复调用得到相同结果。
func TestClassifyExitLabelsAreStable(t *testing.T) {
	allowed := map[ExitClass]bool{
		ExitSuccess: true, ExitFailed: true, ExitTimeout: true,
		ExitSignaled: true, ExitSpawnErr: true, ExitAborted: true,
	}
	for label := range allowed {
		if string(label) == "" {
			t.Fatal("分类标签字面值不应为空")
		}
		if got := ExitClass(string(label)); got != label {
			t.Fatalf("标签往返失败: %q != %q", got, label)
		}
	}

	inputs := []struct {
		code   int
		signal string
		to     bool
		se     bool
		ab     bool
	}{
		{0, "", false, false, false},
		{-1, "SIGKILL", false, false, false},
		{9, "信号中断", false, false, false},
		{0, "", true, false, false},
		{0, "", false, true, false},
		{0, "", false, false, true},
	}
	for _, in := range inputs {
		want := ClassifyExit(in.code, in.signal, in.to, in.se, in.ab)
		if !allowed[want] {
			t.Fatalf("ClassifyExit(%d, %q, ...) = %q, 不是合法标签", in.code, in.signal, want)
		}
		for i := 0; i < 50; i++ {
			if got := ClassifyExit(in.code, in.signal, in.to, in.se, in.ab); got != want {
				t.Fatalf("ClassifyExit(%d, %q, ...) 第 %d 次返回 %q，期望 %q", in.code, in.signal, i, got, want)
			}
		}
	}
}

// TestClassifyExitDoesNotMutateInputs 验证纯函数语义：调用前后入参保持原样，
// 信号串（不可变字符串）的内容也不被改写。
func TestClassifyExitDoesNotMutateInputs(t *testing.T) {
	code := 7
	signal := "用户中断：SIGTERM"
	timedOut, spawnErr, aborted := true, false, true

	wantCode, wantSignal := code, signal
	wantTimedOut, wantSpawnErr, wantAborted := timedOut, spawnErr, aborted

	_ = ClassifyExit(code, signal, timedOut, spawnErr, aborted)

	if code != wantCode || signal != wantSignal {
		t.Fatalf("入参被修改: code=%d signal=%q，期望 %d / %q", code, signal, wantCode, wantSignal)
	}
	if timedOut != wantTimedOut || spawnErr != wantSpawnErr || aborted != wantAborted {
		t.Fatalf("布尔入参被修改: %v/%v/%v，期望 %v/%v/%v",
			timedOut, spawnErr, aborted, wantTimedOut, wantSpawnErr, wantAborted)
	}
}

// TestExitClassIsFailure 验证只有 success 算成功，其余分类（含未知取值）都是失败。
func TestExitClassIsFailure(t *testing.T) {
	tests := []struct {
		class ExitClass
		want  bool
	}{
		{ExitSuccess, false},
		{ExitFailed, true},
		{ExitTimeout, true},
		{ExitSignaled, true},
		{ExitSpawnErr, true},
		{ExitAborted, true},
		{ExitClass("bogus"), true},
		{ExitClass(""), true},
	}
	for _, tt := range tests {
		t.Run(string(tt.class), func(t *testing.T) {
			if got := tt.class.IsFailure(); got != tt.want {
				t.Fatalf("ExitClass(%q).IsFailure() = %v, want %v", tt.class, got, tt.want)
			}
		})
	}
}

// TestExitMarkdown 验证每个分类的稳定标记文本，包括超时与信号的示例格式。
func TestExitMarkdown(t *testing.T) {
	tests := []struct {
		name  string
		class ExitClass
		code  int
		want  string
	}{
		{"success has no marker", ExitSuccess, 0, ""},
		{"failed includes code", ExitFailed, 1, "[failed: 1]"},
		{"failed with negative code", ExitFailed, -1, "[failed: -1]"},
		{"failed with large code", ExitFailed, 130, "[failed: 130]"},
		{"timeout marker", ExitTimeout, 124, "[timed out]"},
		{"timeout marker ignores code", ExitTimeout, 999, "[timed out]"},
		{"signaled marker", ExitSignaled, 9, "[killed by signal: 9]"},
		{"signaled marker with 128 plus signal", ExitSignaled, 137, "[killed by signal: 137]"},
		{"spawn error marker", ExitSpawnErr, -1, "[spawn error]"},
		{"aborted marker", ExitAborted, 0, "[aborted]"},
		{"unknown class has no marker", ExitClass("bogus"), 7, ""},
		{"empty class has no marker", ExitClass(""), 7, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitMarkdown(tt.class, tt.code); got != tt.want {
				t.Fatalf("ExitMarkdown(%q, %d) = %q, want %q", tt.class, tt.code, got, tt.want)
			}
			// 纯函数：重复调用必须逐字节一致。
			for i := 0; i < 20; i++ {
				if got := ExitMarkdown(tt.class, tt.code); got != tt.want {
					t.Fatalf("ExitMarkdown(%q, %d) 第 %d 次返回 %q，期望 %q", tt.class, tt.code, i, got, tt.want)
				}
			}
		})
	}
}

// TestExitMarkdownCJKText 验证标记可安全地拼接到中文输出后，中文内容不被破坏，
// 且标记本身为稳定的 ASCII 方括号文本（便于日志检索）。
func TestExitMarkdownCJKText(t *testing.T) {
	prefix := "命令执行失败：进程被强制终止 "
	for _, class := range []ExitClass{ExitTimeout, ExitSignaled, ExitAborted} {
		mark := ExitMarkdown(class, 9)
		if mark == "" || !strings.HasPrefix(mark, "[") || !strings.HasSuffix(mark, "]") {
			t.Fatalf("ExitMarkdown(%q, 9) = %q，不是方括号标记", class, mark)
		}
		for _, r := range mark {
			if r > 127 {
				t.Fatalf("标记 %q 含非 ASCII 字符 %q，不够稳定", mark, r)
			}
		}
		combined := prefix + mark
		if !strings.HasPrefix(combined, prefix) || !strings.HasSuffix(combined, mark) {
			t.Fatalf("拼接中文文本后标记位置异常: %q", combined)
		}
		if strings.ContainsRune(prefix, '\uFFFD') || strings.ContainsRune(combined, '\uFFFD') {
			t.Fatalf("中文文本在多字节处理中被破坏: %q", combined)
		}
	}

	// 超时与信号的示例文本必须与约定逐字一致。
	if got := ExitMarkdown(ExitTimeout, 0); got != "[timed out]" {
		t.Fatalf("超时标记 = %q, want %q", got, "[timed out]")
	}
	if got := ExitMarkdown(ExitSignaled, 9); got != fmt.Sprintf("[killed by signal: %d]", 9) {
		t.Fatalf("信号标记 = %q", got)
	}
}

// TestClassifyExitAndMarkdownConcurrent 验证分类与标记生成可被并发调用
// 而无需同步；-race 会验证它们没有共享可变状态。
func TestClassifyExitAndMarkdownConcurrent(t *testing.T) {
	cases := []struct {
		code   int
		signal string
		to     bool
		se     bool
		ab     bool
		want   ExitClass
		mark   string
	}{
		{0, "", false, false, false, ExitSuccess, ""},
		{1, "", false, false, false, ExitFailed, "[failed: 1]"},
		{0, "", true, false, false, ExitTimeout, "[timed out]"},
		{9, "信号中断", false, false, false, ExitSignaled, "[killed by signal: 9]"},
		{-1, "", false, true, false, ExitSpawnErr, "[spawn error]"},
		{0, "", false, false, true, ExitAborted, "[aborted]"},
	}
	var wg sync.WaitGroup
	for range 20 {
		for _, tc := range cases {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if got := ClassifyExit(tc.code, tc.signal, tc.to, tc.se, tc.ab); got != tc.want {
					t.Errorf("ClassifyExit(%d, %q, ...) = %q, want %q", tc.code, tc.signal, got, tc.want)
				}
				if got := ExitMarkdown(tc.want, tc.code); got != tc.mark {
					t.Errorf("ExitMarkdown(%q, %d) = %q, want %q", tc.want, tc.code, got, tc.mark)
				}
			}()
		}
	}
	wg.Wait()
}
