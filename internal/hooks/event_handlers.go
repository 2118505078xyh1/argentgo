package hooks

import (
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// eventHandler 是一个事件类型的分发处理器。它把事件专属的元数据
// （何时触发、携带哪些 payload 字段）从 ReportTriggers 的主 switch 中抽离出来：
// 新增事件只需注册一个处理器构造器，无需再改动那份 switch。
//
// 处理器由构造器生成而非全局共享实例：每次查表都得到一份独立的不可变值，
// 因此并发调用 ReportTriggers 不会共享可变状态。
type eventHandler interface {
	// trigger 返回该事件应被触发的时机描述；空串表示没有文档化的触发时机。
	trigger() string
	// payload 返回该事件专属的 payload 字段描述。
	payload() string
}

// eventDescriptor 是 eventHandler 的不可变实现。
// 字段保持私有，只能通过构造器创建，避免处理器在注册后被意外改写。
type eventDescriptor struct {
	triggerText string
	payloadText string
}

func (d eventDescriptor) trigger() string { return d.triggerText }
func (d eventDescriptor) payload() string { return d.payloadText }

// eventHandlerRegistry 保存"事件类型 → 处理器构造器"的静态注册表。
//
// ordered 使用 pluginapi.Registry 提供按 (order, name) 的稳定排序与重名检测；
// byEvent 是按事件名索引的查表视图，使分发侧能直接定位处理器。
// 注册只发生在包 init() 中；init 完成后注册表只读，因此并发查表安全。
type eventHandlerRegistry struct {
	ordered pluginapi.Registry[eventHandler]
	byEvent map[Event]func() eventHandler
}

// newEventHandlerRegistry 返回一个空的注册表。测试用它构造隔离的注册表，
// 生产代码使用包级的 defaultEventHandlers。
func newEventHandlerRegistry() *eventHandlerRegistry {
	return &eventHandlerRegistry{byEvent: make(map[Event]func() eventHandler)}
}

// register 注册一个事件处理器构造器。order 由调用方显式给出，
// 使跨文件 init() 的执行顺序不影响快照顺序。
func (r *eventHandlerRegistry) register(event Event, order int, factory func() eventHandler) error {
	if err := r.ordered.Register(string(event), order, factory); err != nil {
		return fmt.Errorf("hooks: register event %q handler: %w", event, err)
	}
	r.byEvent[event] = factory
	return nil
}

// registerDescriptor 是 register 的便捷形式：把不可变描述符包装成构造器。
// 每次调用构造器都返回一份新的 eventDescriptor 值。
func (r *eventHandlerRegistry) registerDescriptor(event Event, order int, d eventDescriptor) error {
	return r.register(event, order, func() eventHandler { return d })
}

// handlerFor 返回事件对应的处理器构造器；未注册的事件返回 nil。
// 分发侧据此保留"未命中即兜底"的旧行为。
func (r *eventHandlerRegistry) handlerFor(event Event) func() eventHandler {
	return r.byEvent[event]
}

// names 返回按 (order, name) 稳定排序的注册名快照。
func (r *eventHandlerRegistry) names() []string {
	return r.ordered.Names()
}

// factories 返回按 (order, name) 稳定排序的构造器快照。
func (r *eventHandlerRegistry) factories() []func() eventHandler {
	return r.ordered.Factories()
}

// defaultEventHandlers 是包级静态注册表，由下面的 init() 填充。
var defaultEventHandlers = newEventHandlerRegistry()

func init() {
	// order 与 AllEvents 顺序一致（步长 10，便于将来在中间插入事件），
	// 保证诊断/测试中的快照稳定。注册失败属于程序员错误，启动期即暴露。
	entries := []struct {
		event Event
		order int
		desc  eventDescriptor
	}{
		{SessionStart, 10, eventDescriptor{triggerText: "once when a session begins, before the first prompt"}},
		{UserPromptSubmit, 20, eventDescriptor{triggerText: "when the user submits a prompt", payloadText: "Prompt"}},
		{PreToolUse, 30, eventDescriptor{triggerText: "before each tool call; exit 2 blocks the call", payloadText: "Tool, Args, CallID"}},
		{PermissionRequest, 40, eventDescriptor{triggerText: "when a tool call needs an approval decision", payloadText: "Tool, Args, CallID"}},
		{PostToolUse, 50, eventDescriptor{triggerText: "after a tool call succeeds", payloadText: "Tool, Args, Output, CallID"}},
		{PostToolUseFailure, 60, eventDescriptor{triggerText: "after a tool call fails", payloadText: "Tool, Args, Error, CallID"}},
		{Notification, 70, eventDescriptor{triggerText: "when an informational notice is shown to the user", payloadText: "Prompt"}},
		{PreCompact, 80, eventDescriptor{triggerText: "just before the conversation is compacted"}},
		{PostCompact, 81, eventDescriptor{triggerText: "right after the conversation is compacted"}},
		{SessionEnd, 90, eventDescriptor{triggerText: "when a session ends, including on cancellation"}},
		{SubagentStop, 100, eventDescriptor{triggerText: "when a subagent finishes its run", payloadText: "Output"}},
		{Stop, 110, eventDescriptor{triggerText: "when the assistant finishes a turn", payloadText: "Prompt, Output"}},
		{EventInterrupt, 120, eventDescriptor{triggerText: "when the assistant turn is interrupted by the user or transport", payloadText: "Prompt, Output, HookPayload (reason=interrupt)"}},
		{EventStopFailure, 130, eventDescriptor{triggerText: "when the assistant turn ends in a non-cancellation error", payloadText: "Prompt, Output, HookPayload (reason=stop_failure, error text)"}},
		{TurnStart, 115, eventDescriptor{triggerText: "when the assistant starts a turn", payloadText: "Prompt, HookPayload (turn)"}},
		{EventTurnEnd, 140, eventDescriptor{triggerText: "when the assistant turn completes cleanly", payloadText: "Prompt, Output, HookPayload (reason=turn_end)"}},
		{FileChanged, 150, eventDescriptor{triggerText: "when a tool creates, modifies or deletes a file", payloadText: "Path (the affected file)"}},
	}
	for _, entry := range entries {
		if err := defaultEventHandlers.registerDescriptor(entry.event, entry.order, entry.desc); err != nil {
			panic(err)
		}
	}
}

// triggerReportFor 用给定的注册表构建单行报告。查表未命中时只设置 Event，
// 其余字段留空——与原 ReportTriggers 中 switch 无 default 分支的行为逐字节一致。
func triggerReportFor(event Event, registry *eventHandlerRegistry) TriggerReport {
	row := TriggerReport{Event: event}
	if factory := registry.handlerFor(event); factory != nil {
		handler := factory()
		row.Trigger = handler.trigger()
		row.Payload = handler.payload()
	}
	return row
}
