package bash

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSleepGuard 用表驱动锁定前台长 sleep 守卫的判定边界：只有首条
// 前台、字面秒数 >=30 的 sleep 被拒绝；管道、cd 前缀、变量、引号、
// 后台以及短 sleep 一律保守放行。
func TestSleepGuard(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{"foreground 270", "sleep 270", true},
		{"foreground boundary 30", "sleep 30", true},
		{"leading then tail", "sleep 270; tail log", true},
		{"leading and chain", "sleep 270 && tail log", true},
		{"short 5", "sleep 5", false},
		{"cd prefix", "cd x && sleep 30", false},
		{"background", "sleep 300 &", false},
		{"pipe", "sleep 270 | cat", false},
		{"variable", "sleep $N", false},
		{"quoted literal", "sleep '270'", false},
		{"not leading", "echo hi; sleep 270", false},
		{"non sleep", "tail -f log", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, guarded := sleepGuardFor(tc.command)
			if guarded != tc.want {
				t.Fatalf("sleepGuardFor(%q) guarded=%v secs=%d, want guarded=%v", tc.command, guarded, got, tc.want)
			}
			if guarded && got < maxForegroundSleep {
				t.Fatalf("guarded seconds %d < threshold %d", got, maxForegroundSleep)
			}
		})
	}
}

// TestSleepGuardRejectsForegroundInRun 验证默认路径上真正被拒绝：
// 工具返回以 Not run: 开头的错误，并给出 task_output/wait 与 until
// 轮询两种替代，不进入任何执行分支。
func TestSleepGuardRejectsForegroundInRun(t *testing.T) {
	tool := bashTool{}
	_, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{"command":"sleep 270"}`))
	if err == nil || !strings.HasPrefix(err.Error(), "Not run:") {
		t.Fatalf("want Not run: error, got %v", err)
	}
	if !strings.Contains(err.Error(), "task_output/wait") || !strings.Contains(err.Error(), "until <check>") {
		t.Fatalf("want both alternatives, got %q", err.Error())
	}
}

// TestSleepGuardKillSwitch 锁定默认开启 + =0 回退契约：默认不读开关
// 即为开启，只有显式设 0 才关闭，且不留 opt-in 唤醒方式。
func TestSleepGuardKillSwitch(t *testing.T) {
	if !sleepGuardEnabled() {
		t.Fatal("sleep guard should be enabled by default")
	}
	t.Setenv("ARGENT_SLEEP_GUARD", "0")
	if sleepGuardEnabled() {
		t.Fatal("ARGENT_SLEEP_GUARD=0 should disable the guard")
	}
}
