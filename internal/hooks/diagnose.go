package hooks

// 配置完整性诊断。
//
// hooks.go 中的 Validate 检查原始 JSON 文档，捕获解析时的静默拼写错误。
// 解析时的静默拼写错误。本文件补充了一项文档无法触发的检查：
// 本文件补充了一项 Validate 无法触发的检查：Async 和 StopPropagation 是仅供内部使用的
// （json:"-"）标志，由编程方式设置的调用方（内置钩子）赋值，因此对 Validate 不可见。
// DiagnoseConfig 是纯函数 —— 它不读取任何内容、不启动任何进程、从不修改
// 配置；发现的问题被返回，从不记录日志或引发 panic。

import (
	"fmt"
	"strings"
)

// IssueKind 对 Config 中发现的一种完整性问题分类。
type IssueKind string

const (
	// IssueAsyncStopPropagation 标记同时为 Async 和
	// StopPropagation 的命令：一个仍会结束认领 walk 的后台钩子。
	IssueAsyncStopPropagation IssueKind = "async-stop-propagation"
)

// ConfigIssue 是 DiagnoseConfig 的一条发现。Index 是
// 违规命令在 Config.Commands 中从 0 开始的位置；Event 和 Command 复制其事件
// 和文本，使调用方无需重读配置即可定位钩子。
// Message 是稳定的、易读的说明，适合 doctor 输出或
// UI 警告。
type ConfigIssue struct {
	Kind    IssueKind
	Event   Event
	Index   int
	Command string
	Message string
}

// String 将问题渲染为可直接显示的形式："[kind] Event[index]: message"。
func (i ConfigIssue) String() string {
	return fmt.Sprintf("[%s] %s[%d]: %s", i.Kind, i.Event, i.Index, i.Message)
}

// DiagnoseConfig 报告已解析 Config 中的完整性问题。
// 它是只读诊断：不分发任何内容也不修改任何内容，nil 或
// 空配置产生空的（非 nil）切片。
//
// 它检查 Validate 无法触及的唯一维度 —— 仅供内部的
// Async 和 StopPropagation 标志的组合。同时带有两者的命令会结束
// claimedCommands 的 walk 并保护后面每一个钩子，但 Run 永不观察
// 异步钩子的结果（决定性的钩子必须同步回答）。
// 异步钩子的结果（决定性的钩子必须同步回答）。
// 其效果是后面的钩子仅凭一个即发即忘任务就被静默跳过，这几乎总是无意的。
func DiagnoseConfig(c *Config) []ConfigIssue {
	issues := make([]ConfigIssue, 0)
	if c == nil {
		return issues
	}
	for index, command := range c.Commands {
		if !command.Async || !command.StopPropagation {
			continue
		}
		issues = append(issues, ConfigIssue{
			Kind:    IssueAsyncStopPropagation,
			Event:   command.Event,
			Index:   index,
			Command: command.Command,
			Message: fmt.Sprintf(
				"async command %q stops propagation: later %s hooks are skipped, but Run never observes an async decision; make the deciding hook synchronous or remove StopPropagation",
				command.Command, command.Event,
			),
		})
	}
	return issues
}

// FormatConfigIssues 将配置诊断渲染为稳定的、每条问题一行的
// 文本，适合 doctor 输出或 UI 警告。它是纯显示辅助：
// 无 I/O、无修改，空（或 nil）列表产生空字符串，使
// 调用方在没有内容可报告时可以省略整个章节。
//
// 顺序被保留，每行都是 ConfigIssue.String()，其中携带
// 类型、事件、从 0 开始的索引和消息，使读者能定位确切的
// 违规命令。CJK 命令文本和消息被原样复制。
func FormatConfigIssues(issues []ConfigIssue) string {
	if len(issues) == 0 {
		return ""
	}
	lines := make([]string, 0, len(issues))
	for _, issue := range issues {
		lines = append(lines, issue.String())
	}
	return strings.Join(lines, "\n")
}

// diagnoseSummaryFormat 是汇总段的稳定格式：首行给出问题数量，随后是逐条
// 问题文本。拆成常量使 doctor 输出与测试共享同一措辞，避免格式漂移。
const diagnoseSummaryFormat = "hook 配置问题: %d 项\n%s"

// DiagnoseConfigSummary 把配置诊断聚合成一段可直接展示的文本。
// 它是一个纯函数：不读取外部状态、不派发事件、也不修改配置；只调用
// DiagnoseConfig 与 FormatConfigIssues，因此对同一输入始终返回相同的字节。
//
// 存在问题时，首行是稳定的汇总头 "hook 配置问题: N 项"，随后每行是
// FormatConfigIssues 输出的一条问题，行数与问题数 N 一致。没有问题
// （包括 nil 或空配置）时返回空串，调用方据此整体省略该小节。
// 汇总头为简体中文，问题文本原样复制，UTF-8/CJK 安全。
func DiagnoseConfigSummary(c *Config) string {
	issues := DiagnoseConfig(c)
	if len(issues) == 0 {
		return ""
	}
	return fmt.Sprintf(diagnoseSummaryFormat, len(issues), FormatConfigIssues(issues))
}
