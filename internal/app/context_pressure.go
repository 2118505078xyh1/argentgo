package app

import (
	"context"
	"sync/atomic"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/eval"
)

// contextPressureEnabled 控制是否在轮末观察上下文压力，默认关闭。
// 与 agent 的工具参数强转开关一致，这是进程级 opt-in 开关，由 app 装配按需设置：
// 关闭时既不构造 MessageLike、也不调用 MeasurePressure，事件序列（含 seq）与接线前逐字节一致。
var contextPressureEnabled atomic.Bool

// SetContextPressure 打开或关闭轮末上下文压力观察。关闭（默认）时，既有事件流完全不变；
// 打开后仅在轮末按需追加一条 EventNotice，不改动任何既有事件的字节。
func SetContextPressure(enabled bool) {
	contextPressureEnabled.Store(enabled)
}

// contextPressureOn 报告当前是否启用了上下文压力观察。
func contextPressureOn() bool {
	return contextPressureEnabled.Load()
}

// healthyVerdict 是 eval 中"健康"等级的稳定字符串值。eval 刻意不导出该常量，
// 这里以字面量比对，使 eval 包保持零改动。
const healthyVerdict = "healthy"

// pressureNotice 是纯函数：测量 messages 的上下文压力，仅在非健康等级时返回
// 稳定的告警文本；健康时返回空串。est 为 nil，交由 eval 按 Unicode rune 计数，
// 使 CJK/emoji 不会被按字节高估。相同输入总产生相同输出，不修改入参。
func pressureNotice(messages []eval.MessageLike) string {
	p := eval.MeasurePressure(messages, nil)
	if p.Verdict == healthyVerdict {
		return ""
	}
	return "[context pressure] " + p.Summary()
}

// contextPressureObserver 在一次运行期间累积轻量消息，并在轮末调用 notice 钩子。
// notice 是可注入的函数字段，零值（默认）为 nil：此时 collectEvent/observeStop
// 都是空操作，连 MessageLike 都不构造。它由 app 装配按开关决定是否挂接。
type contextPressureObserver struct {
	notice   func([]eval.MessageLike) string
	messages []eval.MessageLike
}

// newContextPressureObserver 按当前开关装配观察器：开启时挂接 pressureNotice，
// 关闭时返回 nil（什么都不挂接）。
func newContextPressureObserver() *contextPressureObserver {
	if !contextPressureOn() {
		return nil
	}
	return &contextPressureObserver{notice: pressureNotice}
}

// collectEvent 从一条 agent 事件中提取诊断所需的轻量消息。notice 为 nil 时立即返回，
// 不构造任何 MessageLike，使关闭路径的开销为零。
func (o *contextPressureObserver) collectEvent(ev agent.Event) {
	if o == nil || o.notice == nil {
		return
	}
	switch ev.Kind {
	case agent.EventTextDelta:
		o.messages = append(o.messages, eval.MessageLike{Role: "assistant", Content: ev.Text})
	case agent.EventToolEnd:
		o.messages = append(o.messages, eval.MessageLike{Role: "tool", Content: ev.Output, ToolResult: true})
	}
}

// observeStop 在轮末把累积消息喂给 notice 钩子；返回的告警文本非空时通过 emit
// 追加一条 EventNotice。notice 为 nil 时不发任何事件。之后清空累积，避免重入重复上报。
func (o *contextPressureObserver) observeStop(emit func(agent.Event)) {
	if o == nil || o.notice == nil {
		return
	}
	if text := o.notice(o.messages); text != "" {
		emit(agent.Event{Kind: agent.EventNotice, Text: text})
	}
	o.messages = nil
}

// contextPressureKey 在运行 context 上承载本次运行的压力观察器。
type contextPressureKey struct{}

// withContextPressure 把观察器附着到 ctx。观察器为 nil 时不写入，
// 使关闭路径与从未接线时完全一致。
func withContextPressure(ctx context.Context, o *contextPressureObserver) context.Context {
	if o == nil {
		return ctx
	}
	return context.WithValue(ctx, contextPressureKey{}, o)
}

// replaceContextPressure 无条件写入本次运行解析出的观察器（含 typed nil），
// 用于覆盖 runner.Prompt 基于进程级旧开关挂接的过期观察器。contextPressureOf
// 对 typed nil 的类型断言返回 nil，collectEvent/observeStop 都是空操作，因此
// 关闭时写入 typed nil 与"从未接线"在运行行为上完全一致。
func replaceContextPressure(ctx context.Context, o *contextPressureObserver) context.Context {
	return context.WithValue(ctx, contextPressureKey{}, o)
}

// contextPressureOf 返回附着在 ctx 上的观察器；没有时返回 nil。
func contextPressureOf(ctx context.Context) *contextPressureObserver {
	o, _ := ctx.Value(contextPressureKey{}).(*contextPressureObserver)
	return o
}
