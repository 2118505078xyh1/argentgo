package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

func TestLoopDetectionEnabledFromEnv(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"unset", "", false},
		{"digit one", "1", true},
		{"true", "true", true},
		{"mixed case", "YES", true},
		{"on with spaces", "  on  ", true},
		{"zero", "0", false},
		{"false", "false", false},
		{"garbage", "maybe", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(string) string { return tc.raw }
			if got := loopDetectionEnabledFromEnv(env); got != tc.want {
				t.Fatalf("loopDetectionEnabledFromEnv(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestConfigureLoopDetectionWiresGlobalSwitch 验证接线：此前 SetLoopDetection
// 只有测试调用，生产路径永远无法开启。这里确认 env 真值能真正设置 agent 的
// 进程级开关，且每次调用都由本次 env 决定（不会被上次运行的残留影响）。
func TestConfigureLoopDetectionWiresGlobalSwitch(t *testing.T) {
	t.Cleanup(func() { agent.SetLoopDetection(false) })

	env := func(v string) func(string) string {
		return func(string) string { return v }
	}

	configureLoopDetection(env("1"))
	if !agent.LoopDetectionEnabled() {
		t.Fatal("ARGENT_LOOP_DETECTION=1 did not enable loop detection")
	}

	configureLoopDetection(env(""))
	if agent.LoopDetectionEnabled() {
		t.Fatal("unset ARGENT_LOOP_DETECTION did not reset loop detection to default off")
	}

	configureLoopDetection(nil)
	if agent.LoopDetectionEnabled() {
		t.Fatal("nil env must leave loop detection off")
	}
}
