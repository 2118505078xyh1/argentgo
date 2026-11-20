// Package hooks 解析兼容 Claude Code 的钩子文档，检查其
// SHA-256 信任，并将生命周期事件分发给 shell 命令。
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Event 是钩子可订阅的一种生命周期事件。
type Event string

// Event 常量是钩子可订阅的生命周期点。名称
// 与 Claude Code 一致，因此现有钩子文档仍能触发。每个
// 每个常量记录预期触发点；完整集合可通过
// 常量上的注释记录其预期触发点；完整集合可通过 ReportTriggers 以报告形式获取。
const (
	// SessionStart 在会话开始、第一个提示之前触发一次。
	SessionStart Event = "SessionStart"
	// UserPromptSubmit 在用户发送提示时触发。
	UserPromptSubmit Event = "UserPromptSubmit"
	// PreToolUse 在工具调用前触发；退出状态 2 可拦截它。
	PreToolUse Event = "PreToolUse"
	// PermissionRequest 在工具调用需要审批决定时触发。
	PermissionRequest Event = "PermissionRequest"
	// PostToolUse 在工具调用成功后触发。
	PostToolUse Event = "PostToolUse"
	// PostToolUseFailure 在工具调用失败后触发。
	PostToolUseFailure Event = "PostToolUseFailure"
	// Notification 在展示给用户的信息性消息时触发。
	Notification Event = "Notification"
	// PreCompact 在对话压缩前触发。
	PreCompact Event = "PreCompact"
	// SessionEnd 在会话结束时触发，包括取消时。
	SessionEnd Event = "SessionEnd"
	// SubagentStop 在子代理完成运行时触发。
	SubagentStop Event = "SubagentStop"
	// Stop 在助手完成一轮时触发。为向后兼容保留；
	// 新调用方应优先使用下面三个回合结束事件，它们说明回合
	// *如何*结束。Stop 仍照常分发。
	Stop Event = "Stop"
	// EventInterrupt 在助手回合被用户中断时触发
	// EventInterrupt 在助手回合被用户或传输层（显式取消或 context.Canceled 错误）中断时触发。
	EventInterrupt Event = "interrupt"
	// EventStopFailure 在助手回合以非取消错误结束时触发
	// EventStopFailure 在助手回合以非取消错误结束时触发：provider 失败、工具错误、截止时间等。
	EventStopFailure Event = "stop_failure"
	// EventTurnEnd 在助手回合干净完成时触发。
	EventTurnEnd Event = "turn_end"
	// FileChanged 在工具创建、修改或删除文件时触发。
	// FileChanged 在工具创建、修改或删除文件时触发。受影响路径在 Payload.Path 中携带。
	FileChanged Event = "FileChanged"
)

// AllEvents 列出所有支持的事件。
var AllEvents = []Event{
	SessionStart,
	UserPromptSubmit,
	PreToolUse,
	PermissionRequest,
	PostToolUse,
	PostToolUseFailure,
	Notification,
	PreCompact,
	SessionEnd,
	SubagentStop,
	Stop,
	EventInterrupt,
	EventStopFailure,
	EventTurnEnd,
	FileChanged,
}

const (
	// defaultTimeout 限制单条钩子命令。
	defaultTimeout = 30 * time.Second
	// minTimeout 是文档 timeoutMs 被限制到的下限。
	minTimeout = 100 * time.Millisecond
)

// ParseEvent 将文档事件名映射为 Event。
func ParseEvent(name string) (Event, bool) {
	for _, event := range AllEvents {
		if string(event) == name {
			return event, true
		}
	}
	return "", false
}

func (e Event) String() string { return string(e) }

// TriggerReport 将生命周期事件与调用方应触发它的时机配对。
// 触发它的时机。它是事件常量上触发时机文档的机器可读形式
// 它是事件常量上触发时机文档的机器可读形式，用于诊断和审计钩子文档
// 可以订阅哪些事件。
type TriggerReport struct {
	Event   Event
	Trigger string // when the event should be dispatched
	Payload string // payload fields that carry event-specific detail
}

// ReportTriggers 按 AllEvents 顺序为每个支持的事件返回一行。
// 报告只是数据：分发事件是调用方的职责。app 层在一次 Execute 前后
// 报告只是数据：分发事件是调用方的职责。app 层在一次 Execute 前后触发
// 会话/提示/工具事件；修改文件的调用方应触发带受影响 Payload.Path 的 FileChanged。
func ReportTriggers() []TriggerReport {
	rows := make([]TriggerReport, 0, len(AllEvents))
	for _, event := range AllEvents {
		row := TriggerReport{Event: event}
		switch event {
		case SessionStart:
			row.Trigger = "once when a session begins, before the first prompt"
		case UserPromptSubmit:
			row.Trigger = "when the user submits a prompt"
			row.Payload = "Prompt"
		case PreToolUse:
			row.Trigger = "before each tool call; exit 2 blocks the call"
			row.Payload = "Tool, Args, CallID"
		case PermissionRequest:
			row.Trigger = "when a tool call needs an approval decision"
			row.Payload = "Tool, Args, CallID"
		case PostToolUse:
			row.Trigger = "after a tool call succeeds"
			row.Payload = "Tool, Args, Output, CallID"
		case PostToolUseFailure:
			row.Trigger = "after a tool call fails"
			row.Payload = "Tool, Args, Error, CallID"
		case Notification:
			row.Trigger = "when an informational notice is shown to the user"
			row.Payload = "Prompt"
		case PreCompact:
			row.Trigger = "just before the conversation is compacted"
		case SessionEnd:
			row.Trigger = "when a session ends, including on cancellation"
		case SubagentStop:
			row.Trigger = "when a subagent finishes its run"
			row.Payload = "Output"
		case Stop:
			row.Trigger = "when the assistant finishes a turn"
			row.Payload = "Prompt, Output"
		case EventInterrupt:
			row.Trigger = "when the assistant turn is interrupted by the user or transport"
			row.Payload = "Prompt, Output, HookPayload (reason=interrupt)"
		case EventStopFailure:
			row.Trigger = "when the assistant turn ends in a non-cancellation error"
			row.Payload = "Prompt, Output, HookPayload (reason=stop_failure, error text)"
		case EventTurnEnd:
			row.Trigger = "when the assistant turn completes cleanly"
			row.Payload = "Prompt, Output, HookPayload (reason=turn_end)"
		case FileChanged:
			row.Trigger = "when a tool creates, modifies or deletes a file"
			row.Payload = "Path (the affected file)"
		}
		rows = append(rows, row)
	}
	return rows
}

// Command 是一条解析后的钩子命令。
type Command struct {
	Event   Event
	Matcher string // empty or "*" matches everything
	Command string
	Timeout time.Duration
	OnError string // "block" (default), "allow" or "ignore"
	Async   bool

	// Priority 在分发时排列钩子：值越大越先运行。它是仅供内部使用的
	// 仅供内部（json:"-"），因此磁盘上的文档格式不变；
	// （json:"-"），因此磁盘上的钩子文档格式不变；文档解析的钩子都从 0 开始并保持
	// 声明顺序。编程调用方（例如内置安全钩子）
	// 声明顺序。编程调用方（例如内置安全钩子）设置正的 Priority 以先于用户钩子运行。
	Priority int `json:"-"`

	// StopPropagation 使分发短路：一旦带此标志的钩子
	// StopPropagation 使分发短路：带此标志的钩子运行后，不再执行更低优先级的钩子。与 Priority 一样它是
	// 仅供内部；文档钩子从不设置它，因此现有文档仍运行所有匹配的钩子。
	// 仅供内部的；文档钩子从不设置它，因此现有文档仍运行所有匹配的钩子。
	StopPropagation bool `json:"-"`
}

// BlocksOnFailure 报告失败的命令是否可以拦截动作。
func (c Command) BlocksOnFailure() bool {
	return c.OnError != "allow" && c.OnError != "ignore"
}

// Config 是从一个或多个文档解析出的钩子。
type Config struct {
	Commands []Command
}

// rawDocument 镜像钩子文档。未知事件被容忍并
// 跳过，与 Claude Code 的宽松加载器一致。
type rawDocument struct {
	Hooks map[string][]json.RawMessage `json:"hooks"`
}

// rawCommand 是一组或一条命令；两种形态共享字段。
type rawCommand struct {
	Type      string       `json:"type"`
	Command   string       `json:"command"`
	Matcher   string       `json:"matcher"`
	TimeoutMS *int64       `json:"timeoutMs"`
	OnError   string       `json:"onError"`
	Async     bool         `json:"async"`
	Hooks     []rawCommand `json:"hooks"`
}

// ParseConfig 解析钩子文档。
func ParseConfig(data []byte) (*Config, error) {
	var document rawDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("hooks: invalid document: %w", err)
	}
	if document.Hooks == nil {
		return nil, errors.New(`hooks: document has no "hooks" object`)
	}
	config := &Config{}
	for _, name := range sortedEventNames(document.Hooks) {
		event, ok := ParseEvent(name)
		if !ok {
			continue
		}
		for _, raw := range document.Hooks[name] {
			commands, err := parseGroup(event, raw)
			if err != nil {
				return nil, err
			}
			config.Commands = append(config.Commands, commands...)
		}
	}
	return config, nil
}

// LoadConfig 读取并解析钩子文档。
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return config, nil
}

func parseGroup(event Event, raw json.RawMessage) ([]Command, error) {
	var group rawCommand
	if err := json.Unmarshal(raw, &group); err != nil {
		return nil, fmt.Errorf("hooks: %s: %w", event, err)
	}
	matcher := strings.TrimSpace(group.Matcher)
	candidates := group.Hooks
	if len(candidates) == 0 {
		candidates = []rawCommand{group}
	}

	commands := make([]Command, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Type != "" && candidate.Type != "command" {
			continue
		}
		text := strings.TrimSpace(candidate.Command)
		if text == "" {
			continue
		}
		commands = append(commands, Command{
			Event:   event,
			Matcher: matcher,
			Command: text,
			Timeout: timeoutOf(candidate.TimeoutMS),
			OnError: onErrorOf(candidate.OnError),
			Async:   candidate.Async,
		})
	}
	return commands, nil
}

func timeoutOf(ms *int64) time.Duration {
	if ms == nil {
		return defaultTimeout
	}
	timeout := time.Duration(*ms) * time.Millisecond
	if timeout < minTimeout {
		return minTimeout
	}
	return timeout
}

func onErrorOf(value string) string {
	switch value {
	case "allow", "ignore", "block":
		return value
	default:
		return "block"
	}
}

// Merge 追加另一个文档的命令。
func (c *Config) Merge(other *Config) {
	if c == nil || other == nil {
		return
	}
	c.Commands = append(c.Commands, other.Commands...)
}

// ForEvent 返回某事件的命令，按工具匹配器过滤。
func (c *Config) ForEvent(event Event, tool string) []Command {
	if c == nil {
		return nil
	}
	commands := make([]Command, 0, len(c.Commands))
	for _, command := range c.Commands {
		if command.Event != event {
			continue
		}
		if command.Matcher == "" || command.Matcher == "*" {
			commands = append(commands, command)
			continue
		}
		if tool == "" {
			continue
		}
		if globMatch(command.Matcher, tool) {
			commands = append(commands, command)
		}
	}
	return commands
}

// Validate 报告宽松解析会吞掉的静默拼写错误。
func Validate(data []byte) []string {
	var document rawDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return []string{"invalid JSON: " + err.Error()}
	}
	if document.Hooks == nil {
		return []string{`hook document has no "hooks" object`}
	}

	var warnings []string
	for _, name := range sortedEventNames(document.Hooks) {
		if _, ok := ParseEvent(name); !ok {
			warnings = append(warnings, fmt.Sprintf("unknown event %q (ignored)", name))
			continue
		}
		for index, raw := range document.Hooks[name] {
			var group rawCommand
			if err := json.Unmarshal(raw, &group); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s.%d: %v", name, index+1, err))
				continue
			}
			if group.Matcher != "" && strings.Contains(group.Matcher, " ") {
				warnings = append(warnings, fmt.Sprintf("%s.%d: matcher %q contains a space", name, index+1, group.Matcher))
			}
			candidates := group.Hooks
			if len(candidates) == 0 {
				candidates = []rawCommand{group}
			}
			for _, candidate := range candidates {
				if candidate.Type != "" && candidate.Type != "command" {
					warnings = append(warnings, fmt.Sprintf("%s.%d: type %q is not supported (ignored)", name, index+1, candidate.Type))
					continue
				}
				if strings.TrimSpace(candidate.Command) == "" {
					warnings = append(warnings, fmt.Sprintf("%s.%d: empty command (ignored)", name, index+1))
				}
				if candidate.OnError != "" && candidate.OnError != "block" &&
					candidate.OnError != "allow" && candidate.OnError != "ignore" {
					warnings = append(warnings, fmt.Sprintf("%s.%d: onError %q is not block|allow|ignore", name, index+1, candidate.OnError))
				}
				if candidate.TimeoutMS != nil && *candidate.TimeoutMS < 100 {
					warnings = append(warnings, fmt.Sprintf("%s.%d: timeoutMs %d is below the 100ms floor", name, index+1, *candidate.TimeoutMS))
				}
			}
		}
	}
	return warnings
}

func sortedEventNames(hooks map[string][]json.RawMessage) []string {
	names := make([]string, 0, len(hooks))
	for name := range hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// globMatch 将 "*" 匹配任意字符序列；其余均按字面。这是钩子文档用于工具名的匹配器语法。
// 其余按字面。这是钩子文档用于工具名的匹配器语法。
func globMatch(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	patternRunes := []rune(pattern)
	valueRunes := []rune(value)
	p, v := 0, 0
	star, starV := -1, 0
	for v < len(valueRunes) {
		switch {
		// 通配符分支必须放在前面。若字面相等分支先运行，
		// 模式中的 '*' 会在值也恰好含 '*' 时被当作字面消耗
		// 通配符分支必须放在前面。若字面相等分支先运行，当值也恰好含 '*' 时，模式中的 '*'
		// 会被当作字面消耗（例如模式 "a*" 对值 "a*b"），导致没有星号可回溯、匹配失败。工具
		// 名和文件路径可以携带字面 '*'，因此无条件把模式的
		// '*' 当作通配符。
		case p < len(patternRunes) && patternRunes[p] == '*':
			star = p
			starV = v
			p++
		case p < len(patternRunes) && patternRunes[p] == valueRunes[v]:
			p++
			v++
		case star >= 0:
			p = star + 1
			starV++
			v = starV
		default:
			return false
		}
	}
	for p < len(patternRunes) && patternRunes[p] == '*' {
		p++
	}
	return p == len(patternRunes)
}
