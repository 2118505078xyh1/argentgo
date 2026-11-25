package app

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/session"
)

// 本文件实现按任务类别路由到物理模型的"虚拟模型"（清单 #3/#6 的模型侧）。
// 虚拟模型名（如 "auto"）是配置里的一组 class→物理模型映射；Execute 每轮按提示
// 的复杂度信号选一个物理模型，并把决策追加为持久事件，便于成本审计与粘性重放。
//
// 它是纯性能优化：不触碰策略/审批/沙箱/工具集；没有配置虚拟模型时零影响。

// TaskClass 是路由使用的任务类别。
type TaskClass string

const (
	TaskSimple   TaskClass = "simple"
	TaskStandard TaskClass = "standard"
	TaskComplex  TaskClass = "complex"
)

const virtualRouteDefault = "auto"

// VirtualRoute 是一个虚拟模型的 class→物理模型映射。
type VirtualRoute struct {
	Simple   string
	Standard string
	Complex  string
}

// ClassifyTask 依据 judge 的纯函数判定与两个复杂度信号得出任务类别。
// UseSecondary 是 judge 已经建议"需要更强模型"的信号，优先级最高。
func ClassifyTask(decision PromptDecision, prompt string, hasCode, mutating bool) TaskClass {
	if decision.UseSecondary {
		return TaskComplex
	}
	if utf8.RuneCountInString(stripPromptNonUserText(prompt)) > 2000 {
		return TaskComplex
	}
	if hasCode || mutating {
		return TaskStandard
	}
	return TaskSimple
}

// forClass 返回某类别映射到的物理模型。
func (v VirtualRoute) forClass(class TaskClass) string {
	switch class {
	case TaskSimple:
		return v.Simple
	case TaskStandard:
		return v.Standard
	case TaskComplex:
		return v.Complex
	}
	return ""
}

// virtualModelsFrom 从配置的原始 map 解析虚拟模型表。未知形态返回空表，
// 使错误配置退化为"不路由"而不是让整次运行失败。
func virtualModelsFrom(raw any) map[string]VirtualRoute {
	out := make(map[string]VirtualRoute)
	table, ok := raw.(map[string]any)
	if !ok {
		return out
	}
	for name, value := range table {
		fields, ok := value.(map[string]any)
		if !ok {
			continue
		}
		route := VirtualRoute{}
		for class, target := range fields {
			model, ok := target.(string)
			if !ok {
				continue
			}
			switch TaskClass(strings.ToLower(strings.TrimSpace(class))) {
			case TaskSimple:
				route.Simple = strings.TrimSpace(model)
			case TaskStandard:
				route.Standard = strings.TrimSpace(model)
			case TaskComplex:
				route.Complex = strings.TrimSpace(model)
			}
		}
		if route.Simple != "" || route.Standard != "" || route.Complex != "" {
			out[strings.TrimSpace(name)] = route
		}
	}
	return out
}

// ResolveVirtualModel 解析目标物理模型。ok 为 true 表示确实改变了主模型；
// 目标与当前主模型相同（或缺失）时不切换，避免无意义的 setModel 与事件写入。
func ResolveVirtualModel(routes map[string]VirtualRoute, name string, class TaskClass, primary string) (string, bool) {
	route, ok := routes[name]
	if !ok {
		return "", false
	}
	target := route.forClass(class)
	if target == "" || target == strings.TrimSpace(primary) {
		return "", false
	}
	return target, true
}

// modelSetter 是 switchableChatter 暴露的最小接缝。手工构建的 chatter 不实现它，
// 此时路由是空操作，保证测试与无配置路径逐字节不变。
type modelSetter interface {
	SetModel(string)
}

// applyVirtualModelRoute 在 Execute 装配阶段应用路由。name 为空表示使用默认
// "auto"；sess 为 nil 时只切换不落盘（一次性、无会话的运行）。
func applyVirtualModelRoute(opts *agent.Options, chatter interface{}, raw any, decision PromptDecision, prompt string, hasCode, mutating bool, sess *session.Session, emit func(agent.Event)) {
	routes := virtualModelsFrom(raw)
	if len(routes) == 0 || opts == nil || chatter == nil {
		return
	}
	name := virtualRouteDefault
	if _, ok := routes[name]; !ok {
		names := make([]string, 0, len(routes))
		for key := range routes {
			names = append(names, key)
		}
		sort.Strings(names)
		name = names[0]
	}
	class := ClassifyTask(decision, prompt, hasCode, mutating)
	model, changed := ResolveVirtualModel(routes, name, class, opts.Model)
	if !changed {
		return
	}
	setter, ok := chatter.(modelSetter)
	if !ok {
		return
	}
	setter.SetModel(model)
	if sess != nil {
		_ = sess.AppendModelRoute(name, string(class), model)
	}
	if emit != nil {
		emit(agent.Event{Kind: agent.EventNotice, ToolName: "model_route", Text: fmt.Sprintf(
			`{"event":"model_route","virtual":%q,"task":%q,"model":%q}`, name, class, model,
		)})
	}
}
