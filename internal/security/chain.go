package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 哨兵错误让调用方可以用 errors.Is 区分硬性拒绝与操作员否决。
var (
	// ErrDenied 表示策略或沙箱拦截了调用。
	ErrDenied = errors.New("security: tool call denied")
	// ErrRefused 表示操作员回答了"否"。
	ErrRefused = errors.New("security: tool call refused by the operator")
	// ErrRecoverablePlanBlock 表示可选的计划模式命令守卫在策略/审批之前拦截了
	// 修改类 shell 命令。与 StopError 不同，它是可恢复的：agent 循环会把它
	// 转成模型可读的工具结果。
	ErrRecoverablePlanBlock = errors.New("security: mutating shell command blocked in plan mode")
	// RestoreToolName 是 checkpoint 恢复（撤销）动作在审计中的名称。
	RestoreToolName = "undo"
)

// StopError 告诉调用方必须停止运行而不是重试：该工具调用被策略拒绝，
// 或被操作员否决。
type StopError struct {
	Tool    string
	Reason  string
	Refused bool
}

// finalitySentence 会附加到每个策略拦截错误上，让模型停止重试一个它无法
// 推翻的拒绝（想法借自 gptme 的 policy-block 钩子）。把它保留为一个共享
// 字符串，使 agent 可见的措辞在策略拒绝和操作员否决之间保持一致。
const finalitySentence = ". This block is intentional and final, not a transient error. " +
	"Do not retry it or reach the same outcome another way."

// Error 实现 error 接口。开头的消息原样保留；只附加终结语句，
// 这样匹配 "denied"/"refused" 的调用方仍然有效。
func (e *StopError) Error() string {
	verb := "denied"
	if e.Refused {
		verb = "refused by the operator"
	}
	var message string
	if e.Reason == "" {
		message = fmt.Sprintf("security: the %s tool call was %s", e.Tool, verb)
	} else {
		message = fmt.Sprintf("security: the %s tool call was %s: %s", e.Tool, verb, e.Reason)
	}
	return message + finalitySentence
}

// Unwrap 把该错误映射到 ErrDenied 或 ErrRefused。
func (e *StopError) Unwrap() error {
	if e.Refused {
		return ErrRefused
	}
	return ErrDenied
}

// FeedbackRefusal 是当操作员拒绝某次调用并留下更正意见（"应该这样做"）时，
// Approver 连同 false 决定一起返回的错误。Approver.Ask 只返回 bool，因此
// 用一个带类型的错误作为这段文本的附加通道：不改变任何现有 Approver 签名，
// 从不产生它的审批器也保留原有的纯 bool 行为。
//
// 链会把它转成模型可读、可据此行动的 RefusalError，而不是终结性的 StopError。
// 单纯的 (false, nil) 回答仍然表示硬性否决。
type FeedbackRefusal struct {
	Feedback string
}

func (e *FeedbackRefusal) Error() string {
	return "security: the operator rejected the tool call with feedback: " + e.Feedback
}

// maxApprovalFeedbackRunes 限制复制到工具错误中的操作员文本长度。
// 反馈是给模型看的；没有上限时，一次回复就可能用任意多的操作员文本填满
// 下一轮的上下文。
const maxApprovalFeedbackRunes = 2000

// RefusalError 是当操作员拒绝某次调用并说明如何修改时产生的可恢复工具错误。
// 它刻意 *不是* StopError：StopError 上的终结语句会让模型放弃该结果，这与
// 可执行的更正意见正好相反。反馈作为普通工具输出到达模型，因此循环继续，
// 模型可以用改动后的请求重试。errors.Is 仍然匹配 ErrRefused。
type RefusalError struct {
	Tool     string
	Feedback string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf(
		"security: the %s tool call was rejected by the operator: %s. Adjust the request and try again.",
		e.Tool, e.Feedback,
	)
}

// Unwrap 为使用 errors.Is 的调用方保留否决哨兵错误。
func (e *RefusalError) Unwrap() error { return ErrRefused }

// clipApprovalFeedback 按 rune 安全地裁剪并标记截断，让模型知道操作员的
// 指令是被缩短了，而不是到那里就结束。
func clipApprovalFeedback(text string) string {
	if utf8.RuneCountInString(text) <= maxApprovalFeedbackRunes {
		return text
	}
	return runeHead(text, maxApprovalFeedbackRunes) + "…[feedback clipped]"
}

// Chain 把策略守卫、审批提示和审计日志组合成工具注册表在运行任何东西之前
// 调用的单一钩子。
//
// 除 Policy（默认为无规则策略）外，所有字段都是可选的。
type Chain struct {
	Policy   *Policy
	Approver Approver
	Audit    *AuditLog
	// Session 标记审计条目；通常是会话文件或 id。
	Session string
	// PlanCommandGuard 为 true 时，会在策略 → 审批 → 审计链之前拦截修改类
	// shell 命令。零值 false 保持原有行为。
	PlanCommandGuard bool
}

// NewChain 构建一个链。nil 审批器表示没有审批通道（无头运行、子 agent），
// 因此需要审批的调用会被报告为不可用 —— 而不是操作员否决。nil 审计日志不
// 记录任何东西。
func NewChain(policy *Policy, approver Approver, audit *AuditLog, session string) *Chain {
	return &Chain{Policy: policy, Approver: approver, Audit: audit, Session: session}
}

// Check 按顺序评估一次工具调用：先是策略，然后审批，最后审计。
// 非 nil 错误表示不得运行该调用。策略拒绝或单纯的操作员否决返回 StopError；
// 带反馈的否决返回 RefusalError，以便模型读取更正意见并用不同方式重试。
// 不可用或已取消的审批通道返回不带终结语句的普通错误：没有人回答，
// 因此该调用并非被否决。
func (c *Chain) Check(ctx context.Context, toolName string, args json.RawMessage) error {
	return c.check(ctx, toolName, args, args)
}

// CheckRestore 让 checkpoint 恢复通过与修改类工具调用相同的策略 → 审批 →
// 审计链。策略看到真实路径，因此拒绝或写入根目录逃逸仍会阻止该动作；
// 审计记录只看到脱敏后的清单路径。
func (c *Chain) CheckRestore(ctx context.Context, paths []string) error {
	raw, err := json.Marshal(map[string]any{"paths": paths})
	if err != nil {
		return fmt.Errorf("security: encoding restore paths: %w", err)
	}
	return c.check(ctx, RestoreToolName, raw, RedactManifestPaths(raw))
}

func (c *Chain) check(ctx context.Context, toolName string, args, auditArgs json.RawMessage) error {
	// 可选的计划模式命令守卫。启用时它最先运行：修改类 shell 命令会作为可恢复
	// 错误被拒绝，早于任何策略拒绝、审批提示或审计条目。默认禁用。
	if c != nil && c.PlanCommandGuard {
		if token := guardShellCommand(toolName, args); token != "" {
			return fmt.Errorf("%w: %q", ErrRecoverablePlanBlock, token)
		}
	}
	name := strings.TrimSpace(toolName)
	if name == "" {
		c.record("", "deny", "the tool name is empty", auditArgs)
		return &StopError{Tool: "", Reason: "the tool name is empty"}
	}
	result := c.policy().Evaluate(name, args)
	switch result.Decision {
	case Deny:
		c.record(name, "deny", result.Reason, auditArgs)
		return &StopError{Tool: name, Reason: result.Reason}
	case Allow:
		c.record(name, "allow", result.Reason, auditArgs)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// nil 的 *Chain 也是合法的关卡：policy() 和 record() 已经兼容 nil 接收者，
	// 上面的计划命令守卫分支也显式检查了 c。对于 Ask 决定，直接通过 c 读取
	// 字段会在这里 panic，因此维持与其他辅助函数相同的 nil 安全承诺。
	var approver Approver
	if c != nil {
		approver = c.Approver
	}
	if approver == nil {
		// 没有审批器与审批器回答"否"不是一回事。使用不可用空对象，
		// 让审计日志记录不可用的通道，而不是错误地声称操作员否决了调用。
		approver = UnavailableApprover{}
	}
	// 在提示中显示真实目标：工作区内的 junction 不能让提示说
	// "home/note.txt"，而工具却写到别处。工具和审计日志仍使用原始参数。
	ok, err := approver.Ask(ctx, name, string(approvalArgs(args, c.policy().Root())))
	if err != nil {
		// 带反馈的拒绝不是审批基础设施故障：它是操作员的更正意见，必须作为
		// 可读、可恢复的工具错误到达模型。不要原始文本写入审计日志；
		// 只有面向模型的错误携带它。
		var corrected *FeedbackRefusal
		if !ok && errors.As(err, &corrected) {
			if feedback := strings.TrimSpace(corrected.Feedback); feedback != "" {
				c.record(name, "refused", "the operator rejected the tool call with feedback", auditArgs)
				return &RefusalError{Tool: name, Feedback: clipApprovalFeedback(feedback)}
			}
			// 空的更正意见只是单纯的"否"，不是提示失败。
			err = nil
		} else {
			// 无法给出回答的通道不算否决。
			// 将其记录为不可用，这样审计日志就不会错误地声称操作员说了"否"，
			// 并且返回的错误不带防重试的终结语句：缺失的通道以后可能出现。
			// 取消是另一种结果，这里不为其记录决定。
			if ClassifyApproval(ok, err) == ApprovalUnavailable {
				c.record(name, "unavailable", "the approval channel is unavailable", auditArgs)
			}
			return fmt.Errorf("security: approval for the %s tool call failed: %w", name, err)
		}
	}
	if !ok {
		c.record(name, "refused", "the operator refused the tool call", auditArgs)
		return &StopError{Tool: name, Reason: "the operator refused the tool call", Refused: true}
	}
	// 对 Approver.Ask 的原始回答做失败即拒绝的规范化。上面每个分支都已经把
	// 错误（包括已取消或不可用的审批通道）转成返回的失败，因此对于当前的
	// 审批器这个守卫不可达，也不会改变任何现有 deny/ask/allow 事件。
	// 它是一条防御性不变量：未来或机器审批路径永远无法把意外的 (granted, err)
	// 组合当作同意混过来。只有真正的 ApprovalAllowed 才能到达下面的 allow
	// 记录（dsh/goose 审计）。
	if ClassifyApproval(ok, err) != ApprovalAllowed {
		c.record(name, "deny", "approval was not granted", auditArgs)
		return &StopError{Tool: name, Reason: "approval was not granted"}
	}
	c.record(name, "allow", "approved by the operator", auditArgs)
	return nil
}

func (c *Chain) policy() *Policy {
	if c == nil || c.Policy == nil {
		return defaultPolicy()
	}
	return c.Policy
}

var fallbackPolicy = DefaultPolicy(".")

func defaultPolicy() *Policy { return fallbackPolicy }

func (c *Chain) record(tool, decision, reason string, args json.RawMessage) {
	if c == nil || c.Audit == nil {
		return
	}
	entry := AuditEntry{
		Tool:     tool,
		Decision: decision,
		Reason:   reason,
		Session:  c.Session,
	}
	if len(args) > 0 {
		entry.Args = RedactJSON(args)
	}
	_ = c.Audit.Record(entry)
}

// approvalArgs 把路径参数替换为其在磁盘上的真实位置，用于审批提示中显示。
// 它只用于显示：工具仍接收原始参数，审计记录也保留原始参数。当路径无法解析，
// 或已经指向其真实位置时，返回原始 JSON 不变，使普通调用的提示逐字节一致。
func approvalArgs(args json.RawMessage, root string) json.RawMessage {
	decoder := json.NewDecoder(strings.NewReader(string(args)))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return args
	}
	changed := false
	rewrite := func(value string) string {
		resolved := value
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, resolved)
		}
		real, err := ResolveRealPath(resolved)
		if err != nil || filepath.Clean(real) == filepath.Clean(resolved) {
			return value
		}
		changed = true
		return real
	}
	for _, key := range pathArgKeys {
		if value, ok := fields[key].(string); ok {
			fields[key] = rewrite(value)
		}
	}
	for _, key := range pathArrayKeys {
		if list, ok := fields[key].([]any); ok {
			for i, item := range list {
				if value, ok := item.(string); ok {
					list[i] = rewrite(value)
				}
			}
		}
	}
	if !changed {
		return args
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return args
	}
	return encoded
}
