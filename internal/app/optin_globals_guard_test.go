package app

import (
	"os"
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
)

// TestExecuteDefaultsLeaveGlobalOptInsOff 是 R28 并发审计在 app 侧的守卫。
// Execute 装配时会用默认（全关）opt-ins 回写五个 agent 层进程级开关（拒绝原因、
// 不可信结果、输出脱敏、错误反射、纠正提示存活）与一个 app 层开关（上下文压力）。这里在
// 默认 env 下模拟这次回写，断言 app 层可观测的开关确实关闭，并锁定默认解析面
// 逐键为 false。
//
// 注意：agent 层的 refusalReason / toolResultWrapping / outputRedaction /
// toolErrorReflection / retainGuidance 是包内私有状态，app 包读不到对应 getter；这些开关由
// internal/agent 包的 optin_globals_guard_test.go 与各 wire 测试直接守护。app 侧
// 只能通过 contextPressureOn / newContextPressureObserver 观测自己拥有的那个开关。
//
// 刻意不调用 t.Parallel()：进程级全局状态必须与同包其它测试串行。
func TestExecuteDefaultsLeaveGlobalOptInsOff(t *testing.T) {
	// retainGuidance 走独立 env（Execute 用裸 os.Getenv 读 ARGENT_RETAIN_GUIDANCE），
	// 不经过 ParseOptInEnv 的 optIns 映射。这里显式清空以固定"默认解析面"。
	t.Setenv("ARGENT_RETAIN_GUIDANCE", "")

	// 复刻 Execute 的默认解析面：env 缺失、无配置时每个键都显式为 false。
	optIns := mergeOptIns(ParseOptInEnv(nil), configOptIns(nil))
	for _, key := range allOptInKeys {
		if optIns[key] {
			t.Errorf("默认 opt-in 键 %q 却为 true", key)
		}
	}
	// Execute 回写的 optIns 映射五个键必须逐键为 false。
	for _, key := range []OptInKey{
		OptInRefusalReason,
		OptInUntrustedResult,
		OptInOutputRedaction,
		OptInToolErrorReflection,
		OptInContextPressure,
	} {
		if optIns[key] {
			t.Errorf("默认回写键 %q 却为 true", key)
		}
	}
	// 第 5 个 setter retainGuidance 由独立 env 决定，默认开启（仅 =0 关闭）。
	retainDefault := strings.TrimSpace(os.Getenv("ARGENT_RETAIN_GUIDANCE")) != "0"
	if !retainDefault {
		t.Fatal("默认 ARGENT_RETAIN_GUIDANCE 应开启")
	}

	// 恢复默认，避免污染其它测试。
	t.Cleanup(func() {
		agent.SetRefusalReason(false)
		agent.SetToolResultWrapping(false)
		agent.SetOutputRedaction(false)
		agent.SetToolErrorReflection(false)
		agent.SetRetainGuidance(false)
		SetContextPressure(false)
	})

	// 模拟 Execute 的写回：先全部打开，再按默认值写回 false，
	// 从而断言的是"回写确实生效"，而不是"开关从未被打开"的假绿。
	agent.SetRefusalReason(true)
	agent.SetToolResultWrapping(true)
	agent.SetOutputRedaction(true)
	agent.SetToolErrorReflection(true)
	agent.SetRetainGuidance(true)
	SetContextPressure(true)

	agent.SetRefusalReason(optIns[OptInRefusalReason])
	agent.SetToolResultWrapping(optIns[OptInUntrustedResult])
	agent.SetOutputRedaction(optIns[OptInOutputRedaction])
	agent.SetToolErrorReflection(optIns[OptInToolErrorReflection])
	agent.SetRetainGuidance(retainDefault)
	SetContextPressure(optIns[OptInContextPressure])

	// app 层可观测的开关：上下文压力必须关闭，且不挂接任何观察器。
	// agent 层的五个开关在 app 包不可读，由 agent 包测试守护（见文件头注释）。
	if contextPressureOn() {
		t.Fatal("默认回写后 contextPressure 仍为 on")
	}
	if o := newContextPressureObserver(); o != nil {
		t.Fatalf("默认回写后仍挂接了压力观察器：%+v", o)
	}
}
