package proc

import (
	"sync"
	"testing"
)

func TestClassifyExitCode(t *testing.T) {
	tests := []struct {
		name   string
		code   int
		signal string
		want   string
	}{
		{"zero is success", 0, "", "success"},
		{"130 is interrupted", 130, "", "interrupted"},
		{"124 is timeout", 124, "", "timeout"},
		{"generic nonzero is error", 1, "", "error"},
		{"negative code is error", -1, "", "error"},
		{"large code is error", 42, "", "error"},
		// 信号优先于所有数字退出码。
		{"signal wins over success", 0, "SIGTERM", "signal"},
		{"signal wins over interrupted", 130, "SIGINT", "signal"},
		{"signal wins over timeout", 124, "SIGTERM", "signal"},
		{"signal wins over error", 7, "SIGKILL", "signal"},
		// CJK 信号必须安全：只判空，不按字节切分。
		{"single cjk signal", 9, "中断", "signal"},
		{"multi cjk signal with zero code", 0, "信号：用户已取消", "signal"},
		{"cjk signal with special code", 130, "中断信号", "signal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyExitCode(tt.code, tt.signal); got != tt.want {
				t.Fatalf("classifyExitCode(%d, %q) = %q, want %q", tt.code, tt.signal, got, tt.want)
			}
		})
	}
}

// 验证稳定性：相同输入重复调用得到相同结果，且只从给定的五个标签中取值。
func TestClassifyExitCodeIsStable(t *testing.T) {
	inputs := []struct {
		code   int
		signal string
	}{
		{0, ""},
		{130, ""},
		{124, ""},
		{1, ""},
		{-1, "SIGKILL"},
		{0, "用户中断"},
	}
	allowed := map[string]bool{
		"success": true, "interrupted": true, "timeout": true,
		"error": true, "signal": true,
	}
	for _, in := range inputs {
		want := classifyExitCode(in.code, in.signal)
		if !allowed[want] {
			t.Fatalf("classifyExitCode(%d, %q) = %q, 不是合法的分类标签", in.code, in.signal, want)
		}
		for i := 0; i < 50; i++ {
			if got := classifyExitCode(in.code, in.signal); got != want {
				t.Fatalf("classifyExitCode(%d, %q) 第 %d 次返回 %q，期望稳定的 %q", in.code, in.signal, i, got, want)
			}
		}
	}
}

// 纯函数可被并发调用；-race 会验证它没有共享可变状态。
func TestClassifyExitCodeConcurrent(t *testing.T) {
	cases := []struct {
		code   int
		signal string
		want   string
	}{
		{0, "", "success"},
		{130, "", "interrupted"},
		{124, "", "timeout"},
		{2, "", "error"},
		{9, "信号中断", "signal"},
	}
	var wg sync.WaitGroup
	for range 20 {
		for _, tc := range cases {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got := classifyExitCode(tc.code, tc.signal)
				if got != tc.want {
					t.Errorf("classifyExitCode(%d, %q) = %q, want %q", tc.code, tc.signal, got, tc.want)
				}
			}()
		}
	}
	wg.Wait()
}
