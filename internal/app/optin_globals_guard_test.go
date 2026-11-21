package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// TestExecuteDefaultsLeaveGlobalOptInsOff 是 R28 并发审计在 app 侧的守卫。
// Execute 装配时会用默认（全关）opt-ins 回写三个 agent 层进程级开关与一个 app 层
// 开关。这里在默认 env 下模拟这次回写，断言 app 层可观测的开关确实关闭，并锁定
// 默认解析面逐键为 false。
//
// 注意：agent 层的 refusalReason / toolResultWrapping / toolErrorReflection 是包内
// 私有状态，app 包读不到对应 getter；这些开关由 internal/agent 包的
// optin_globals_guard_test.go 与各 wire 测试直接守护。app 侧只能通过
// contextPressureOn / newContextPressureObserver 观测自己拥有的那个开关。
//
// 刻意不调用 t.Parallel()：进程级全局状态必须与同包其它测试串行。
func TestExecuteDefaultsLeaveGlobalOptInsOff(t *testing.T) {
	// 复刻 Execute 的默认解析面：env 缺失、无配置时每个键都显式为 false。
	optIns := mergeOptIns(ParseOptInEnv(nil), configOptIns(nil))
	for _, key := range allOptInKeys {
		if optIns[key] {
			t.Errorf("默认 opt-in 键 %q 却为 true", key)
		}
	}
	// Execute 真正回写的四个键必须逐键为 false。
	for _, key := range []OptInKey{
		OptInRefusalReason,
		OptInUntrustedResult,
		OptInToolErrorReflection,
		OptInContextPressure,
	} {
		if optIns[key] {
			t.Errorf("默认回写键 %q 却为 true", key)
		}
	}

	// 恢复默认，避免污染其它测试。
	t.Cleanup(func() {
		agent.SetRefusalReason(false)
		agent.SetToolResultWrapping(false)
		agent.SetToolErrorReflection(false)
		SetContextPressure(false)
	})

	// 模拟 Execute 的写回：先全部打开，再按默认 opt-ins 写回 false，
	// 从而断言的是"回写确实生效"，而不是"开关从未被打开"的假绿。
	agent.SetRefusalReason(true)
	agent.SetToolResultWrapping(true)
	agent.SetToolErrorReflection(true)
	SetContextPressure(true)

	agent.SetRefusalReason(optIns[OptInRefusalReason])
	agent.SetToolResultWrapping(optIns[OptInUntrustedResult])
	agent.SetToolErrorReflection(optIns[OptInToolErrorReflection])
	SetContextPressure(optIns[OptInContextPressure])

	// app 层可观测的开关：上下文压力必须关闭，且不挂接任何观察器。
	// agent 层的三个开关在 app 包不可读，由 agent 包测试守护（见文件头注释）。
	if contextPressureOn() {
		t.Fatal("默认回写后 contextPressure 仍为 on")
	}
	if o := newContextPressureObserver(); o != nil {
		t.Fatalf("默认回写后仍挂接了压力观察器：%+v", o)
	}
}
