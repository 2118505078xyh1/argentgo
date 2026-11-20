package proc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTimeoutResult(t *testing.T) {
	deadline := fmt.Errorf("proc: %q exceeded %s: %w", "sleep", 2*time.Second, context.DeadlineExceeded)
	nestedDeadline := fmt.Errorf("tool: %w", deadline)
	canceled := fmt.Errorf("proc: %q: %w", "sleep", context.Canceled)
	plain := errors.New("proc: spawn failed")

	tests := []struct {
		name          string
		elapsed       time.Duration
		limit         time.Duration
		err           error
		wantTimedOut  string
		wantElapsedMS string
		wantLimitMS   string
		wantMessage   string
	}{
		{
			name:          "超时的错误标记为 timed_out",
			elapsed:       2500 * time.Millisecond,
			limit:         2 * time.Second,
			err:           deadline,
			wantTimedOut:  "true",
			wantElapsedMS: "2500",
			wantLimitMS:   "2000",
			wantMessage:   deadline.Error(),
		},
		{
			name:          "多层包装的超时仍标记为 timed_out",
			elapsed:       3 * time.Second,
			limit:         2 * time.Second,
			err:           nestedDeadline,
			wantTimedOut:  "true",
			wantElapsedMS: "3000",
			wantLimitMS:   "2000",
			wantMessage:   nestedDeadline.Error(),
		},
		{
			name:          "无错误时不标记超时",
			elapsed:       500 * time.Millisecond,
			limit:         2 * time.Second,
			err:           nil,
			wantTimedOut:  "false",
			wantElapsedMS: "500",
			wantLimitMS:   "2000",
			wantMessage:   "",
		},
		{
			name:          "普通错误不标记超时",
			elapsed:       100 * time.Millisecond,
			limit:         2 * time.Second,
			err:           plain,
			wantTimedOut:  "false",
			wantElapsedMS: "100",
			wantLimitMS:   "2000",
			wantMessage:   plain.Error(),
		},
		{
			name:          "用户取消不标记超时",
			elapsed:       150 * time.Millisecond,
			limit:         2 * time.Second,
			err:           canceled,
			wantTimedOut:  "false",
			wantElapsedMS: "150",
			wantLimitMS:   "2000",
			wantMessage:   canceled.Error(),
		},
		{
			name:          "零值边界",
			elapsed:       0,
			limit:         0,
			err:           nil,
			wantTimedOut:  "false",
			wantElapsedMS: "0",
			wantLimitMS:   "0",
			wantMessage:   "",
		},
		{
			name:          "耗时等于上限的边界",
			elapsed:       2 * time.Second,
			limit:         2 * time.Second,
			err:           nil,
			wantTimedOut:  "false",
			wantElapsedMS: "2000",
			wantLimitMS:   "2000",
			wantMessage:   "",
		},
		{
			name:          "亚毫秒向下取整",
			elapsed:       1500 * time.Microsecond,
			limit:         1999 * time.Microsecond,
			err:           nil,
			wantTimedOut:  "false",
			wantElapsedMS: "1",
			wantLimitMS:   "1",
			wantMessage:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := timeoutResult(tt.elapsed, tt.limit, tt.err)
			if len(got) != 4 {
				t.Fatalf("timeoutResult 返回 %d 个字段，期望 4 个: %v", len(got), got)
			}
			for key, want := range map[string]string{
				"timed_out":  tt.wantTimedOut,
				"elapsed_ms": tt.wantElapsedMS,
				"limit_ms":   tt.wantLimitMS,
				"message":    tt.wantMessage,
			} {
				if value := got[key]; value != want {
					t.Errorf("timeoutResult()[%q] = %q，期望 %q", key, value, want)
				}
			}
		})
	}
}

func TestTimeoutResultClampsNegativeDurations(t *testing.T) {
	got := timeoutResult(-5*time.Second, -3*time.Second, nil)
	if got["elapsed_ms"] != "0" {
		t.Errorf("负耗时应钳到 0，得到 %q", got["elapsed_ms"])
	}
	if got["limit_ms"] != "0" {
		t.Errorf("负上限应钳到 0，得到 %q", got["limit_ms"])
	}

	got = timeoutResult(-time.Second, 2*time.Second, nil)
	if got["elapsed_ms"] != "0" || got["limit_ms"] != "2000" {
		t.Errorf("只有负耗时应被钳到 0，得到 %v", got)
	}

	// 钳零后仍按 err 判定超时，不受负数耗时影响。
	got = timeoutResult(-time.Second, -time.Second, context.DeadlineExceeded)
	if got["timed_out"] != "true" {
		t.Errorf("负耗时场景下的超时判定被改变: %v", got)
	}
}

func TestTimeoutResultCJKMessage(t *testing.T) {
	const want = "proc: 命令执行超时：中文与 emoji 🕒 都要原样保留"
	err := fmt.Errorf("%s: %w", want, context.DeadlineExceeded)

	got := timeoutResult(2*time.Second, time.Second, err)
	if got["message"] != err.Error() {
		t.Errorf("message 未原样保留: 得到 %q，期望 %q", got["message"], err.Error())
	}
	if !utf8.ValidString(got["message"]) {
		t.Errorf("message 不是合法的 UTF-8: %q", got["message"])
	}
	if strings.ContainsRune(got["message"], utf8.RuneError) {
		t.Errorf("message 中出现了 U+FFFD 替换字符: %q", got["message"])
	}
	if got["timed_out"] != "true" {
		t.Errorf("带中文的超时错误未被判定为超时: %v", got)
	}
}

func TestRemainingBudget(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		total   time.Duration
		want    time.Duration
	}{
		{"剩余充足", time.Second, 5 * time.Second, 4 * time.Second},
		{"无耗时", 0, 3 * time.Second, 3 * time.Second},
		{"刚好耗尽", 5 * time.Second, 5 * time.Second, 0},
		{"已经超支", 6 * time.Second, 5 * time.Second, 0},
		{"零预算", 0, 0, 0},
		{"纳秒级剩余", 999 * time.Millisecond, time.Second, time.Millisecond},
		{"负耗时钳零", -time.Second, 3 * time.Second, 3 * time.Second},
		{"负总预算返回零", time.Second, -2 * time.Second, 0},
		{"负耗时与零预算返回零", -time.Second, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remainingBudget(tt.elapsed, tt.total); got != tt.want {
				t.Errorf("remainingBudget(%s, %s) = %s，期望 %s", tt.elapsed, tt.total, got, tt.want)
			}
		})
	}
}

func TestRemainingBudgetSharedDeadline(t *testing.T) {
	// 两个阶段共享同一条截止时间：第一阶段耗时直接从总预算中扣除，
	// 第二阶段拿到的必须是剩余预算，而不是重新启动的 5 秒。
	const total = 5 * time.Second
	firstStage := 2 * time.Second
	secondBudget := remainingBudget(firstStage, total)
	if secondBudget != 3*time.Second {
		t.Fatalf("第一阶段后剩余预算 = %s，期望 3s", secondBudget)
	}

	secondStage := time.Second
	finalBudget := remainingBudget(secondStage, secondBudget)
	if finalBudget != 2*time.Second {
		t.Fatalf("第二阶段后剩余预算 = %s，期望 2s", finalBudget)
	}

	// 一旦超支，后续阶段一律拿到 0，不会复活出新的预算。
	if got := remainingBudget(3*time.Second, secondBudget); got != 0 {
		t.Fatalf("超支后的剩余预算 = %s，期望 0", got)
	}
}
