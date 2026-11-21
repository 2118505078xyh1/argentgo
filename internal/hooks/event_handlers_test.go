package hooks

import (
	"strings"
	"testing"
)

// TestEventHandlerRegistryRegistrationOrderIsStable 验证乱序注册时
// 快照仍按 (order, name) 稳定排序，与插件 API 注册表的语义一致。
func TestEventHandlerRegistryRegistrationOrderIsStable(t *testing.T) {
	registry := newEventHandlerRegistry()
	entries := []struct {
		event Event
		order int
		value string
	}{
		{Event("charlie"), 2, "c"},
		{Event("alpha"), 1, "a"},
		{Event("bravo"), 1, "b"},
	}
	// 故意逆序注册，排除"按注册先后输出"的偶然正确。
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if err := registry.register(entry.event, entry.order, func() eventHandler {
			return eventDescriptor{triggerText: entry.value}
		}); err != nil {
			t.Fatalf("register %s: %v", entry.event, err)
		}
	}

	wantNames := []string{"alpha", "bravo", "charlie"}
	gotNames := registry.names()
	if len(gotNames) != len(wantNames) {
		t.Fatalf("names() = %v, want %v", gotNames, wantNames)
	}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("names() = %v, want %v", gotNames, wantNames)
		}
	}

	factories := registry.factories()
	if len(factories) != 3 {
		t.Fatalf("factories() length = %d, want 3", len(factories))
	}
	wantValues := []string{"a", "b", "c"}
	for i, factory := range factories {
		if got := factory().trigger(); got != wantValues[i] {
			t.Fatalf("factories()[%d] = %q, want %q", i, got, wantValues[i])
		}
	}
}

// TestEventHandlerRegistryEventMapping 验证事件到处理器的映射逐字节沿用
// 原 ReportTriggers switch 的输出：触发器描述与 payload 字段描述完全一致。
func TestEventHandlerRegistryEventMapping(t *testing.T) {
	want := []TriggerReport{
		{SessionStart, "once when a session begins, before the first prompt", ""},
		{UserPromptSubmit, "when the user submits a prompt", "Prompt"},
		{PreToolUse, "before each tool call; exit 2 blocks the call", "Tool, Args, CallID"},
		{PermissionRequest, "when a tool call needs an approval decision", "Tool, Args, CallID"},
		{PostToolUse, "after a tool call succeeds", "Tool, Args, Output, CallID"},
		{PostToolUseFailure, "after a tool call fails", "Tool, Args, Error, CallID"},
		{Notification, "when an informational notice is shown to the user", "Prompt"},
		{PreCompact, "just before the conversation is compacted", ""},
		{SessionEnd, "when a session ends, including on cancellation", ""},
		{SubagentStop, "when a subagent finishes its run", "Output"},
		{Stop, "when the assistant finishes a turn", "Prompt, Output"},
		{EventInterrupt, "when the assistant turn is interrupted by the user or transport", "Prompt, Output, HookPayload (reason=interrupt)"},
		{EventStopFailure, "when the assistant turn ends in a non-cancellation error", "Prompt, Output, HookPayload (reason=stop_failure, error text)"},
		{EventTurnEnd, "when the assistant turn completes cleanly", "Prompt, Output, HookPayload (reason=turn_end)"},
		{FileChanged, "when a tool creates, modifies or deletes a file", "Path (the affected file)"},
	}

	// 每个事件都必须有构造器，且构造出的处理器产出旧 switch 的字面值。
	for index, event := range AllEvents {
		factory := defaultEventHandlers.handlerFor(event)
		if factory == nil {
			t.Fatalf("event %q has no registered handler", event)
		}
		handler := factory()
		if got, wantTrigger := handler.trigger(), want[index].Trigger; got != wantTrigger {
			t.Errorf("event %q trigger = %q, want %q", event, got, wantTrigger)
		}
		if got, wantPayload := handler.payload(), want[index].Payload; got != wantPayload {
			t.Errorf("event %q payload = %q, want %q", event, got, wantPayload)
		}
	}

	// 重构后的 ReportTriggers 必须与旧 switch 的期望行逐字节一致。
	got := ReportTriggers()
	if len(got) != len(want) {
		t.Fatalf("ReportTriggers() has %d rows, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("ReportTriggers()[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
}

// TestEventHandlerRegistryMissFallsBack 验证未命中注册表的事件保留
// 原 switch 无 default 分支的兜底行为：只设置 Event，其余字段留空。
func TestEventHandlerRegistryMissFallsBack(t *testing.T) {
	registry := newEventHandlerRegistry()
	if factory := registry.handlerFor(Event("never-registered")); factory != nil {
		t.Fatal("unregistered event must not resolve to a handler")
	}

	// 即使是默认注册表里已有的事件，换一个空注册表也必须走兜底路径。
	for _, event := range []Event{FileChanged, Stop, Event("unknown")} {
		got := triggerReportFor(event, registry)
		want := TriggerReport{Event: event}
		if got != want {
			t.Errorf("triggerReportFor(%q) = %+v, want %+v", event, got, want)
		}
	}
}

// TestEventHandlerRegistryRejectsDuplicate 验证重复注册返回错误，
// 且失败后原有构造器保持不变（注册表不被部分写入污染）。
func TestEventHandlerRegistryRejectsDuplicate(t *testing.T) {
	registry := newEventHandlerRegistry()
	original := func() eventHandler { return eventDescriptor{triggerText: "original"} }
	if err := registry.register(PreToolUse, 1, original); err != nil {
		t.Fatalf("first registration: %v", err)
	}

	for _, order := range []int{1, 99} {
		err := registry.register(PreToolUse, order, func() eventHandler {
			return eventDescriptor{triggerText: "duplicate"}
		})
		if err == nil {
			t.Fatalf("duplicate registration with order %d must fail", order)
		}
		if !strings.Contains(err.Error(), string(PreToolUse)) || !strings.Contains(err.Error(), "register event") {
			t.Fatalf("duplicate error should name the event, got %q", err)
		}
	}

	factory := registry.handlerFor(PreToolUse)
	if factory == nil {
		t.Fatal("original handler lost after failed duplicate registration")
	}
	if got := factory().trigger(); got != "original" {
		t.Fatalf("original handler was overwritten: trigger = %q", got)
	}
	if names := registry.names(); len(names) != 1 || names[0] != string(PreToolUse) {
		t.Fatalf("names() = %v, want exactly [%s]", names, PreToolUse)
	}
}

// TestDefaultEventHandlerRegistryCoversAllEvents 验证编译期 init 注册
// 覆盖了全部事件，且数量与 AllEvents 一致，没有多注册或漏注册。
func TestDefaultEventHandlerRegistryCoversAllEvents(t *testing.T) {
	if got, want := len(defaultEventHandlers.factories()), len(AllEvents); got != want {
		t.Fatalf("registered handlers = %d, want %d", got, want)
	}
	for _, event := range AllEvents {
		if defaultEventHandlers.handlerFor(event) == nil {
			t.Errorf("event %q not registered", event)
		}
	}
	// 注册表快照按显式 order 排序，order 与 AllEvents 顺序一致。
	names := defaultEventHandlers.names()
	for i, event := range AllEvents {
		if names[i] != string(event) {
			t.Errorf("names()[%d] = %q, want %q", i, names[i], event)
		}
	}
}

// TestEventHandlerRegistryFactoriesAreConstructors 验证注册表保存的是
// 构造器而非共享实例：多次调用都返回携带描述符的独立处理器值。
func TestEventHandlerRegistryFactoriesAreConstructors(t *testing.T) {
	registry := newEventHandlerRegistry()
	// 用指针实现注册，才能验证每次调用构造器都产生不同的实例。
	if err := registry.register(Event("constructors"), 0, func() eventHandler {
		return &eventDescriptor{triggerText: "value"}
	}); err != nil {
		t.Fatal(err)
	}
	factory := registry.handlerFor(Event("constructors"))
	first, second := factory(), factory()
	if first.trigger() != "value" || second.trigger() != "value" {
		t.Fatalf("constructors returned unexpected values: %q %q", first.trigger(), second.trigger())
	}
	if first == second {
		t.Fatal("two constructor calls should not return the same handler instance")
	}
}
