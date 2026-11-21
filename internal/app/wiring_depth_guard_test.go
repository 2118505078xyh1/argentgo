package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// 本文件是 R13「opt-in 激活面」的守护层，只新增测试、不改任何生产逻辑。
// 它锁定默认关闭契约：env 缺失/为空/未知键时全部开关为 false，未 Set 的键 Enabled 为
// false；三个进程级 setter 关闭时各自消费（classifyRefusal、appendTool 属 agent 内部，
// 由 agent 包的 wire 测试直接守护；app 侧守护其可观测的事件/消息字节）走零值路径；
// wireVerifyFail 关闭时返回 nil、OnVerifyFail 不装配；三个压缩相关 app 纯函数关闭时
// 原样返回字符串。所有 setter 用 defer 恢复 false，避免污染其它测试。

// guardDefaultOffRegistry 复刻 Execute 装配面的注册表构造：把全部受支持键显式 Set 为
// false（与 DefaultOptIns 逐键一致）。这是「默认关闭」在注册表中的真实形态。
func guardDefaultOffRegistry() *OptInRegistry {
	reg := new(OptInRegistry)
	for _, key := range allOptInKeys {
		reg.Set(key, false)
	}
	return reg
}

// guardCaptureStream 复刻 context_pressure_test.go 中 capturePressureStream 的事件采集
// 模式（同一条 hub 发布/订阅路径、记录每条事件 seq、轮末上报），但：一是用 guard 前缀
// 命名以避免冲突，二是用 replaceContextPressure 模拟 Execute 对 runner.Prompt 过期观察器
// 的无条件覆盖。这样守护的是「默认关闭时观察器为 typed nil、事件流逐字节不变」。
func guardCaptureStream(observer *contextPressureObserver, events []agent.Event) ([]agent.Event, []uint64, uint64) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()
	ctx := withEventEmit(context.Background(), func(e agent.Event) { hub.Publish(e) })
	ctx = replaceContextPressure(ctx, observer)
	obs := contextPressureOf(ctx)

	seqs := make([]uint64, 0, len(events))
	for _, ev := range events {
		EmitEvent(ctx, ev)
		seqs = append(seqs, hub.Seq())
		obs.collectEvent(ev)
	}
	obs.observeStop(func(e agent.Event) { EmitEvent(ctx, e) })
	finalSeq := hub.Seq()

	var got []agent.Event
	for {
		select {
		case e := <-ch:
			got = append(got, e)
		default:
			return got, seqs, finalSeq
		}
	}
}

// guardTestSession 构造一个最小真实会话，仅用于验证关闭路径下 wireVerifyFail 的返回。
func guardTestSession(t *testing.T) *session.Session {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	sess, err := store.NewSession("wiring-guard")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// TestGuardParseOptInEnvDefaultOff 守护解析面的默认关闭契约：env 为 nil、空值、仅分隔符
// 或全为未知键时，结果必须与 DefaultOptIns 逐键一致（全 false）。
func TestGuardParseOptInEnvDefaultOff(t *testing.T) {
	want := DefaultOptIns()
	tests := []struct {
		name string
		env  func(string) string
	}{
		{"nil env", nil},
		{"empty value", func(string) string { return "" }},
		{"only commas and spaces", func(string) string { return "  , , \t, " }},
		{"unknown keys only", func(string) string { return "no_such_key,另一个键,bogus" }},
		{"mixed known-looking but unknown", func(string) string { return "REFUSAL_REASON,CONTEXT_PRESSURE,unknown_key " }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOptInEnv(tc.env)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ParseOptInEnv(%s) = %v, want all false %v", tc.name, got, want)
			}
		})
	}
}

// TestGuardOptInRegistryUnsetKeysOff 守护注册表 fail-closed 契约：零值注册表未 Set 的键
// 全部 Enabled=false；Execute 用全 false 显式 Set 后同样全部关闭。
func TestGuardOptInRegistryUnsetKeysOff(t *testing.T) {
	var zero OptInRegistry
	appReg := guardDefaultOffRegistry()
	for _, key := range allOptInKeys {
		if zero.Enabled(key) {
			t.Errorf("零值注册表未 Set 的键 %q 却为 enabled", key)
		}
		if appReg.Enabled(key) {
			t.Errorf("默认关闭注册表键 %q 却为 enabled", key)
		}
	}
}

// TestGuardDefaultOffWiringKeepsStreamByteIdentical 守护核心字节不变性：先模拟上一轮曾
// 打开三个进程级开关，再用默认 opt-in（全 false）写回，断言观察器被摘除、事件序列（含
// seq）与「从未接线」的基线逐字节一致。classifyRefusal/appendTool 是 agent 内部的消费者，
// 其零值路径由 agent 包测试守护；本测试锁定 app 激活面关闭后对外可观测的字节不变。
func TestGuardDefaultOffWiringKeepsStreamByteIdentical(t *testing.T) {
	// 模拟上一轮残留的开状态。
	agent.SetRefusalReason(true)
	agent.SetToolResultWrapping(true)
	SetContextPressure(true)
	// 每个 setter 都用 defer 恢复默认 false，避免污染其它测试。
	defer agent.SetRefusalReason(false)
	defer agent.SetToolResultWrapping(false)
	defer SetContextPressure(false)

	// 复刻 Execute 的激活面：解析默认 opt-ins 并把三个 setter 写回 false。
	optIns := ParseOptInEnv(nil)
	agent.SetRefusalReason(optIns[OptInRefusalReason])
	agent.SetToolResultWrapping(optIns[OptInUntrustedResult])
	SetContextPressure(optIns[OptInContextPressure])

	if contextPressureOn() {
		t.Fatal("默认 opt-in 后 contextPressure 开关仍为 on")
	}
	if o := newContextPressureObserver(); o != nil {
		t.Fatalf("默认 opt-in 后仍挂接了压力观察器：%+v", o)
	}

	base := []agent.Event{
		{Kind: agent.EventTurnStart, Turn: 1},
		{Kind: agent.EventTextDelta, Turn: 1, Text: "你好，世界"},
		{Kind: agent.EventToolStart, Turn: 1, ToolName: "read"},
		{Kind: agent.EventToolEnd, Turn: 1, ToolName: "read", Output: "file body\n"},
		{Kind: agent.EventUsage, Turn: 1},
		{Kind: agent.EventStop, Reason: agent.StopComplete},
	}
	gotBase, seqBase, finalBase := guardCaptureStream(nil, base)
	gotOff, seqOff, finalOff := guardCaptureStream(newContextPressureObserver(), base)

	if !reflect.DeepEqual(gotOff, gotBase) {
		t.Fatalf("默认关闭时事件字节发生变化:\n got %+v\nwant %+v", gotOff, gotBase)
	}
	if !reflect.DeepEqual(seqOff, seqBase) || finalOff != finalBase {
		t.Fatalf("默认关闭时 seq 发生变化: seqs %v/%v, final %d/%d", seqOff, seqBase, finalOff, finalBase)
	}
}

// TestGuardWireVerifyFailOffNotAssembled 守护失败尝试隔离的关闭契约：nil 注册表、零值
// 注册表、Execute 默认关闭注册表都让 wireVerifyFail 返回 nil，调用方据此不装配
// OnVerifyFail（app_execute.go 中直接赋值），验证失败只走既有 notice 路径。
func TestGuardWireVerifyFailOffNotAssembled(t *testing.T) {
	sess := guardTestSession(t)
	otherOnly := new(OptInRegistry)
	otherOnly.Set(OptInUntrustedResult, true)

	tests := []struct {
		name string
		sess *session.Session
		reg  *OptInRegistry
	}{
		{"nil registry", sess, nil},
		{"zero registry", sess, new(OptInRegistry)},
		{"app default-off registry", sess, guardDefaultOffRegistry()},
		{"other key on", sess, otherOnly},
		{"nil session", nil, func() *OptInRegistry {
			reg := new(OptInRegistry)
			reg.Set(OptInAttemptIsolation, true)
			return reg
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wireVerifyFail(tc.sess, tc.reg); got != nil {
				t.Fatal("wireVerifyFail() = non-nil, want nil（关闭时不应装配 OnVerifyFail）")
			}
		})
	}
}

// TestGuardThreeWiresPassThroughWhenOff 守护三个压缩相关 app 纯函数的默认关闭契约：
// nil 注册表、零值注册表、Execute 默认关闭注册表下，观察者不挂接、字符串原样返回
// （含空串、尾部空白与 CJK 字节序列）。
func TestGuardThreeWiresPassThroughWhenOff(t *testing.T) {
	regs := []struct {
		name string
		reg  *OptInRegistry
	}{
		{"nil registry", nil},
		{"zero registry", new(OptInRegistry)},
		{"app default-off registry", guardDefaultOffRegistry()},
	}
	stringsToKeep := []string{"", "  summary with trailing space  ", "重建摘要\n你好，世界"}
	for _, tc := range regs {
		t.Run(tc.name, func(t *testing.T) {
			// compact_fidelity：关闭时不挂接观察者。
			if got := newCompactionFidelityObserver(tc.reg); got != nil {
				t.Fatalf("newCompactionFidelityObserver() = non-nil, want nil")
			}
			// 完整摘要本身也不产生告警，保持零字节变化。
			complete := "## Compaction\n" +
				"\n## Goal\n- 修复登录鉴权\n" +
				"\n## Files changed\n- internal/auth.go\n" +
				"\n## Failures\n- failed: permission denied\n" +
				"\n## Next steps\n- 补单元测试\n"
			if got := compactFidelityNotice(complete); got != "" {
				t.Fatalf("完整摘要告警 = %q, want 空串", got)
			}

			// implicit_context：关闭时原样返回 summary。
			for _, s := range stringsToKeep {
				if got := enrichWithImplicit(s, nil, tc.reg); got != s {
					t.Fatalf("enrichWithImplicit(%q) = %q, want 原样", s, got)
				}
			}

			// summary_sections：关闭时原样返回 rebuilt。
			for _, s := range stringsToKeep {
				if got := preserveSummarySections(s, "## Goal\n- 原始目标", tc.reg); got != s {
					t.Fatalf("preserveSummarySections(%q) = %q, want 原样", s, got)
				}
			}
		})
	}
}
