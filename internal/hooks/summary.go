package hooks

import "sort"

// EventSummary 是 PendingEventSummary 的一行：单个生命周期事件的注册
// 单个生命周期事件的注册统计。它是纯数据；构建摘要
// 统计。它是纯数据；构建摘要从不分发或修改钩子。
type EventSummary struct {
	Event       Event
	Count       int // total hooks registered for the event
	WithMatcher int // hooks carrying a specific matcher (non-empty, not "*")
	Async       int // hooks marked for background execution
}

// PendingEventSummary 报告每个至少有一个注册钩子的事件的钩子数量、带特定匹配器的
// 钩子，它有多少钩子、多少带特定匹配器、多少
// 数量以及异步数量。这是用于 CLI `doctor` 输出或
// 调试：它描述配置了什么要触发，而非已经触发了什么。
//
// 调试的只读诊断：它描述配置了什么要触发，而非已经触发了什么。
// 行按 AllEvents 顺序；AllEvents 之外（由编程调用方设置的事件，例如内置钩子）按名称排最后。空或 nil 配置
// 产生 nil。
func (c *Config) PendingEventSummary() []EventSummary {
	if c == nil || len(c.Commands) == 0 {
		return nil
	}

	byEvent := make(map[Event]*EventSummary, len(c.Commands))
	for _, command := range c.Commands {
		row := byEvent[command.Event]
		if row == nil {
			row = &EventSummary{Event: command.Event}
			byEvent[command.Event] = row
		}
		row.Count++
		if command.Matcher != "" && command.Matcher != "*" {
			row.WithMatcher++
		}
		if command.Async {
			row.Async++
		}
	}

	rows := make([]EventSummary, 0, len(byEvent))
	for _, event := range AllEvents {
		if row := byEvent[event]; row != nil {
			rows = append(rows, *row)
			delete(byEvent, event)
		}
	}

	unknown := make([]Event, 0, len(byEvent))
	for event := range byEvent {
		unknown = append(unknown, event)
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	for _, event := range unknown {
		rows = append(rows, *byEvent[event])
	}
	return rows
}
