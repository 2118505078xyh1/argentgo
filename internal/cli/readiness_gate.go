package cli

import (
	"strings"
)

// 常见组件名，供调用方在构造 readinessCheck 时复用，避免各处手写字符串漂移。
const (
	readinessComponentConfig    = "config"
	readinessComponentProvider  = "provider"
	readinessComponentWorkspace = "workspace"
)

// readinessCheck 是单个组件的就绪输入。name 标识组件（config、provider、
// workspace 等）；ready 表示是否就绪；reason 在未就绪时说明原因，就绪时被
// 聚合器忽略。各组件的就绪状态由调用方探测后传入，本包不亲自探测。
type readinessCheck struct {
	name   string
	ready  bool
	reason string
}

// notReadyItem 是结构化的未就绪项，记录未就绪组件的名称与原因。结构化而非
// 拼接字符串，使调用方能按字段读取、排序或翻译，无需解析文本。
type notReadyItem struct {
	component string
	reason    string
}

// readinessReport 是就绪聚合的结果。ready 为 true 表示所有组件均就绪；
// pending 收集所有未就绪项，保持传入顺序，且仅在有未就绪项时非 nil。
type readinessReport struct {
	ready   bool
	pending []notReadyItem
}

// aggregateReadiness 聚合各组件的就绪状态，实现启动就绪门控的判定核心：
// 只有全部组件就绪才判定为 ready，否则给出结构化的未就绪清单。
//
// 它是纯函数：只读参数，不修改传入切片，不读取配置、会话或全局状态，不联系
// provider、不检查工作区，也不产生任何 I/O；各组件的探测结果由调用方通过
// checks 传入。因此既能安全地用于表驱动测试，也能在将来由启动路径 opt-in
// 调用，而不改变当前任何命令的字节级输出（当前尚未接线，见接线点报告）。
//
// 返回值约定：
//   - 空清单（nil 或长度为 0）：没有需要等待的组件，视为就绪；
//   - 所有组件 ready：ready=true，pending 为空；
//   - 任一组件未就绪：ready=false，pending 按输入顺序收集全部未就绪项，
//     包括 name 或 reason 为空的项，字段原样保留以便调用方定位。
func aggregateReadiness(checks []readinessCheck) readinessReport {
	report := readinessReport{ready: true}
	for _, check := range checks {
		if !check.ready {
			report.ready = false
			report.pending = append(report.pending, notReadyItem{
				component: check.name,
				reason:    check.reason,
			})
		}
	}
	return report
}

// String 返回可读的聚合摘要：就绪时为 "ready"（与门控通过的状态词一致）；
// 未就绪时按 pending 的顺序拼接出中文摘要。它是纯函数，输出稳定且仅由字段名
// 与固定文案构成；不把组件探测的原始细节嵌套进文本之外的地方。
func (r readinessReport) String() string {
	if r.ready {
		return "ready"
	}
	parts := make([]string, 0, len(r.pending))
	for _, item := range r.pending {
		if item.reason == "" {
			parts = append(parts, item.component)
		} else {
			parts = append(parts, item.component+"："+item.reason)
		}
	}
	return "未就绪：" + strings.Join(parts, ", ")
}
