package app

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
)

// TestGuidanceDedupeEnvGate 锁定默认开、=0 关的解析规则（值允许首尾空白）：
// nil 与缺省视为开；修剪后为 "0" 才关闭；其它值（空/"1"/"true" 等）一律开启。
func TestGuidanceDedupeEnvGate(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, true},
		{"unset", envFrom(nil), true},
		{"on", envFrom(map[string]string{"ARGENT_GUIDANCE_DEDUPE": "1"}), true},
		{"on with spaces", envFrom(map[string]string{"ARGENT_GUIDANCE_DEDUPE": " 1 "}), true},
		{"empty value", envFrom(map[string]string{"ARGENT_GUIDANCE_DEDUPE": ""}), true},
		{"true word", envFrom(map[string]string{"ARGENT_GUIDANCE_DEDUPE": "true"}), true},
		{"zero", envFrom(map[string]string{"ARGENT_GUIDANCE_DEDUPE": "0"}), false},
		{"zero with spaces", envFrom(map[string]string{"ARGENT_GUIDANCE_DEDUPE": " 0 "}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := guidanceDedupeEnabled(c.env); got != c.want {
				t.Fatalf("guidanceDedupeEnabled = %v, want %v", got, c.want)
			}
		})
	}
}

// runGuidanceDedupeExecute 用最简脚本化运行驱动一次 Execute，让装配点的
// SetGuidanceDedupe 真正执行；结果经导出读端观测，避免只测解析函数的假绿。
func runGuidanceDedupeExecute(t *testing.T, dedupe string) bool {
	t.Helper()
	base, root, _ := wiringEnv(t)
	env := func(key string) string {
		if key == "ARGENT_GUIDANCE_DEDUPE" {
			return dedupe
		}
		return base(key)
	}
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.Approve = autoApprover{}
	res.Chatter = &scriptedChatter{replies: []string{"done"}}
	store := testStore(t, res)
	sess, err := store.NewSession("guidance-dedupe-wire")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "hello", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return agent.GuidanceDedupeEnabled()
}

// TestExecuteWiresGuidanceDedupe 证明 Execute 装配点确实把 env 写进 agent 的
// 进程级开关：缺省写回 true，只有 =0 才关闭。刻意不并行，避免与同包其它
// 依赖默认开关状态的测试相互污染。
func TestExecuteWiresGuidanceDedupe(t *testing.T) {
	t.Cleanup(func() { agent.SetGuidanceDedupe(false) })

	// 先手工关闭，再以缺省 env 运行一次：断言的是"回写确实生效"，
	// 而不是"开关从未被打开"的假绿。
	agent.SetGuidanceDedupe(false)
	if got := runGuidanceDedupeExecute(t, ""); !got {
		t.Fatal("Execute with default env did not enable guidance dedupe")
	}
	if got := runGuidanceDedupeExecute(t, "0"); got {
		t.Fatal("Execute with ARGENT_GUIDANCE_DEDUPE=0 left guidance dedupe on")
	}
}
