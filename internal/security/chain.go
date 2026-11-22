package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
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
	// shell 命令。零值 false 保持原有行为。它是遗留的直接赋值开关，保留以兼容
	// 现有调用方；运行期切换请用 EnablePlanMode。
	PlanCommandGuard bool
	// EnableCommandRisk 为 true 时，check 会在策略判定之前对 bash/powershell
	// 命令做静态风险分级：forbidden 直接 StopError（不可被审批绕过），
	// destructive 即使策略允许也强制走 Ask 并在否决理由中写风险档；并在调用
	// Approver.Ask 之前，对需要显式确认的命令施加可选审批器接口的拒绝。
	// 零值 false 保持原有行为逐字节不变。它由 app 层的 command_risk opt-in 装配。
	EnableCommandRisk bool
	// ForceAsk 是可选的「即便策略允许也必须询问」判定：返回 true 时 Allow 分支
	// 落到正常审批路径（不是硬性拒绝）。它由 app 层按 MCP 工具的 destructiveHint
	// opt-in 注入；nil 时该分支逐字节不变。它只作用于已知工具名，不改变策略判定。
	ForceAsk func(name string) bool
	// Preview 为可选的审批差异预览回调：返回一段简短的改动摘要（如 +N/-M），
	// 追加到审批提示的参数文本中，让文本审批器也能看到修改内容。
	// 它不改变审批判定；nil 时提示文本逐字节不变。由 app 层按 opt-in 注入。
	Preview func(name string, args json.RawMessage) string
	// ApprovalTimeout 是单次审批等待（含分阶段审批的整个交互过程）允许的最长
	// 时间。零值（含 NewChain 构造与 &Chain{} 零值）表示套用默认 5 分钟；超过
	// 30 分钟会被钳制，负数同样按未配置处理。超时会让审批器收到 DeadlineExceeded，
	// 由 ClassifyApproval 归为 ApprovalCancelled，绝不会误判为批准。
	// 该字段只影响等待审批的那段时间，不修改调用方传入的父 context。
	ApprovalTimeout time.Duration
	// planMode 是 EnablePlanMode 管理的原子启用标志（0=关闭，1=开启）。它只通过
	// sync/atomic 包函数读写，因此既是原子的、又不会像 atomic.Bool 那样因嵌入
	// noCopy 而让按值复制 Chain 触发 go vet 的 copylocks 警告。它与 PlanCommandGuard
	// 按"或"关系参与守卫判断：旧的直接赋值仍然有效，新 API 额外提供并发安全的
	// 运行时开关。零值 0 保持原有行为。
	planMode uint32
	// stagedApproval 是 EnableStagedApproval 管理的原子启用标志（0=关闭，1=开启）。
	// 与 planMode 同理只用 sync/atomic 读写，因此并发切换安全、按值复制不触发
	// copylocks 警告。开启后由 AdvanceStage 状态机接管审批决策；零值 0 保持原有
	// (bool, error) 审批路径逐字节不变。
	stagedApproval uint32
}

// NewChain 构建一个链。nil 审批器表示没有审批通道（无头运行、子 agent），
// 因此需要审批的调用会被报告为不可用 —— 而不是操作员否决。nil 审计日志不
// 记录任何东西。
func NewChain(policy *Policy, approver Approver, audit *AuditLog, session string) *Chain {
	return &Chain{Policy: policy, Approver: approver, Audit: audit, Session: session}
}

// StagedApprover 是可选的分阶段审批通道。它在一次工具调用的审批内与操作员
// 多次交互，每次返回一条 ApprovalDecision 交给 AdvanceStage 推进阶段；always
// 二次确认通过后，RememberScope 收到确认过的作用域，由审批器负责真正"记住"。
//
// 它是纯附加接口：不改动 Approver 签名，普通审批器不实现它。即使 opt-in 打开，
// 缺少该实现时 check 仍走原来的 (bool, error) 路径，逐字节不变。
type StagedApprover interface {
	Approver
	AskStage(ctx context.Context, name string, args string, stage ApprovalStage, scopeSummary string) (ApprovalDecision, error)
	RememberScope(scopes []string)
}

// ExplicitYesApprover 是可选接口：实现它的审批器声明自己对高危 shell 命令
// 必须取得显式 yes，不能被 yolo/auto 静默批准。它不改动 Approver 的签名：
// 普通审批器不实现它，链在 opt-in 关闭或审批器未实现时逐字节走原有路径。
// 当命令被 ShellConfirmationRequired 判定为需要确认且该方法返回 true 时，
// 链会在调用 Ask 之前直接拒绝，避免把 Ask 变成无人工参与的自动批准。
type ExplicitYesApprover interface {
	RequiresExplicitYes() bool
}

// EnableStagedApproval 打开或关闭 Chain 的分阶段审批状态机。nil 接收者是无操作。
//
// 默认关闭；只有审批器同时实现 StagedApprover 时才会真正接管审批决策。与
// EnablePlanMode 一样使用原子标志，可在运行期被并发切换而不触发数据竞争。
func (c *Chain) EnableStagedApproval(enabled bool) {
	if c == nil {
		return
	}
	var value uint32
	if enabled {
		value = 1
	}
	atomic.StoreUint32(&c.stagedApproval, value)
}

// StagedApprovalEnabled 报告 c 当前是否启用了分阶段审批。只读、并发安全，供
// 诊断使用；nil 接收者返回 false。
func StagedApprovalEnabled(c *Chain) bool {
	return c != nil && atomic.LoadUint32(&c.stagedApproval) != 0
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

// chainShellCommand 从 shell 工具的 JSON 参数中提取命令文本。解析规则与
// command_guard.go 的 guardShellCommand 完全一致：只认 bash/powershell 的
// command（其次 script）字段；其他工具或畸形负载返回 ""（安全的假阴性）。
// 这里不调用 guardShellCommand，因为它会应用 ARGENT_GUARDRAILS 三态护栏并
// 返回 token 而非原始命令；风险分级需要在 opt-in 之外完全跳过那条路径。
func chainShellCommand(toolName string, args json.RawMessage) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name != "bash" && name != "powershell" {
		return ""
	}
	var payload struct {
		Command string `json:"command"`
		Script  string `json:"script"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return ""
	}
	if strings.TrimSpace(payload.Command) == "" {
		return payload.Script
	}
	return payload.Command
}

// explicitYesBlockReason 报告审批器是否声明对需要显式确认的 command 拒绝
// 自动批准，并返回 ShellConfirmationRequired 给出的理由；否则返回 ""。
// 它是纯判定：不改 Approver 签名，未实现可选接口的审批器恒返回 ""。
func explicitYesBlockReason(approver Approver, command string) string {
	if command == "" {
		return ""
	}
	required, reason := ShellConfirmationRequired(command)
	if !required {
		return ""
	}
	if explicit, ok := approver.(ExplicitYesApprover); ok && explicit.RequiresExplicitYes() {
		return reason
	}
	return ""
}

// riskTierReason 给理由附上风险档，便于模型与审计日志区分"普通否决"和
// "按静态风险分级拦截"。命令未被分级（opt-in 关闭或非破坏性）时原样返回，
// 保证默认运行的每一字节理由都不变。
func riskTierReason(reason string, risk CommandRisk) string {
	switch risk {
	case CommandRiskForbidden, CommandRiskDestructive:
		return fmt.Sprintf("%s（风险档：%s）", reason, risk)
	default:
		return reason
	}
}

func (c *Chain) check(ctx context.Context, toolName string, args, auditArgs json.RawMessage) error {
	// 可选的计划模式命令守卫。启用时它最先运行：修改类 shell 命令会作为可恢复
	// 错误被拒绝，早于任何策略拒绝、审批提示或审计条目。默认禁用。
	if c != nil && (c.PlanCommandGuard || atomic.LoadUint32(&c.planMode) != 0) {
		if token := guardShellCommand(toolName, args); token != "" {
			return fmt.Errorf("%w: %q", ErrRecoverablePlanBlock, token)
		}
	}
	name := strings.TrimSpace(toolName)
	// 命令风险分级 opt-in：默认关闭时整段跳过，策略 → 审批 → 审计逐字节不变。
	// 启用后解出 shell 命令并静态分级；forbidden 直接硬性拒绝（任何审批器都
	// 无法绕过），destructive 记下风险档，使策略的 Allow 也无法跳过审批。
	var command string
	var commandRisk CommandRisk
	if c != nil && c.EnableCommandRisk {
		command = chainShellCommand(toolName, args)
		if command != "" {
			commandRisk = ClassifyCommand(command)
			if commandRisk == CommandRiskForbidden {
				reason := riskTierReason("the shell command was blocked by static risk classification", commandRisk)
				c.record(name, "deny", reason, auditArgs)
				return &StopError{Tool: name, Reason: reason}
			}
		}
	}
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
		// destructive 档即便策略允许也必须落到下面的审批路径；声明显式确认的
		// 审批器在只读但需确认的命令（如 sudo）上同样不能走 Allow 快捷返回。
		// opt-in 关闭时两个条件都为假，Allow 分支逐字节不变。
		blockReason := ""
		if c != nil && c.EnableCommandRisk {
			blockReason = explicitYesBlockReason(c.Approver, command)
		}
		// MCP annotations opt-in：destructive 工具即便策略允许也必须询问。
		forceAsk := c != nil && c.ForceAsk != nil && c.ForceAsk(name)
		if commandRisk == CommandRiskDestructive || blockReason != "" || forceAsk {
			// 落到下面的审批路径（显式确认的拒绝会在 Ask 之前发生）。
		} else {
			c.record(name, "allow", result.Reason, auditArgs)
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// nil 的 *Chain 也是合法的关卡：policy() 和 record() 已经兼容 nil 接收者，
	// 上面的计划命令守卫分支也显式检查了 c。对于 Ask 决定，直接通过 c 读取
	// 字段会在这里 panic，因此维持与其他辅助函数相同的 nil 安全承诺。
	var approver Approver
	var approvalWait time.Duration
	if c != nil {
		approver = c.Approver
		approvalWait = c.ApprovalTimeout
	}
	if approver == nil {
		// 没有审批器与审批器回答"否"不是一回事。使用不可用空对象，
		// 让审计日志记录不可用的通道，而不是错误地声称操作员否决了调用。
		approver = UnavailableApprover{}
	}
	// 显式确认 opt-in：需要操作员显式确认的命令若遇到声明 RequiresExplicitYes
	// 的审批器（yolo/auto 会把 Ask 变成静默批准），在 Ask 之前直接拒绝并附理由。
	// 默认关闭、命令不需确认或审批器未实现可选接口时，下面流程逐字节不变。
	if c != nil && c.EnableCommandRisk {
		if reason := explicitYesBlockReason(approver, command); reason != "" {
			c.record(name, "deny", reason, auditArgs)
			return &StopError{Tool: name, Reason: reason}
		}
	}
	// 分阶段审批 opt-in：仅在显式开启、且审批器实现 StagedApprover 时接管审批。
	// 默认关闭（或普通审批器）时，下面整段原有 (bool, error) 审批逻辑逐字节不变。
	if StagedApprovalEnabled(c) {
		if staged, ok := approver.(StagedApprover); ok {
			return c.checkStaged(ctx, name, args, auditArgs, staged)
		}
	}
	// 在提示中显示真实目标：工作区内的 junction 不能让提示说
	// "home/note.txt"，而工具却写到别处。工具和审计日志仍使用原始参数。
	// 审批等待必须有上限：无头运行下没有人应答时，直接把 ctx 交给 Ask 会无限期
	// 挂起。这里派生一个只用于本次 Ask 的子 context；cancel 释放计时器，且不会
	// 取消父 context，后续步骤仍可继续使用同一个 ctx。
	// 钩子预审批（opt-in 由 app 层打章控制）：PreToolUse 返回 allow 裁决时，
	// app 把工具名与参数字节打进 ctx。查章命中则跳过弹窗，但策略与审计照常执行。
	// 没有章时这一段只是一次类型断言，默认审批路径逐字节不变。
	if HookApproved(ctx, name, string(args)) {
		c.record(name, "allow", "pre-approved by a PreToolUse hook", auditArgs)
		return nil
	}
	askCtx, cancel := withApprovalTimeout(ctx, approvalWait)
	defer cancel()
	askText := string(approvalArgs(args, c.policy().Root()))
	// 审批差异预览（opt-in）：在提示参数后附加一段有界摘要，不改变判定逻辑。
	if c != nil && c.Preview != nil {
		if summary := strings.TrimSpace(c.Preview(name, args)); summary != "" {
			askText += " (" + clipApprovalFeedback(summary) + ")"
		}
	}
	ok, err := approver.Ask(askCtx, name, askText)
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
		// destructive 档的否决理由附上风险档，便于模型与审计区分按风险拦截。
		// opt-in 关闭时 riskTierReason 原样返回，拒绝文案逐字节不变。
		reason := riskTierReason("the operator refused the tool call", commandRisk)
		c.record(name, "refused", reason, auditArgs)
		return &StopError{Tool: name, Reason: reason, Refused: true}
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

// maxStagedApprovalRounds 限制一次分阶段审批的交互轮数，防止异常审批器让状态机
// 无限循环（例如一直回退或一直不确认）。超出上限按硬性拒绝处理，绝不放行。
const maxStagedApprovalRounds = 32

// checkStaged 用 AdvanceStage 状态机驱动一次分阶段审批。它只负责推进阶段与
// 解释完成结果：反馈文本复用现有 RefusalError / clipApprovalFeedback 通道，
// 作用域仅在 always 二次确认通过后才交给 RememberScope 记忆。
//
// 迁移完全交给纯函数 AdvanceStage：stageOnce 选 always 进入二次确认、选 reject
// 进入反馈收集；Esc 由状态机回退到上一阶段（永不视为完成）；只有 Confirmed 才
// 提交。完成时的阶段决定结果：once 仅放行一次；always 确认后记住作用域并放行；
// reject 把收集到的反馈填进 RefusalError，成为模型可读、可据此调整的可恢复错误。
func (c *Chain) checkStaged(ctx context.Context, name string, args, auditArgs json.RawMessage, staged StagedApprover) error {
	stage := stageOnce
	var scope []string
	var feedback string
	prompt := string(approvalArgs(args, c.policy().Root()))

	// 与普通审批一样给整个分阶段交互设上限，避免某一轮 AskStage 无人应答时
	// 永久挂起。子 context 只在本次审批内使用，不改动父 ctx。
	var approvalWait time.Duration
	if c != nil {
		approvalWait = c.ApprovalTimeout
	}
	askCtx, cancel := withApprovalTimeout(ctx, approvalWait)
	defer cancel()

	for round := 0; round < maxStagedApprovalRounds; round++ {
		// 检查 askCtx 而不是父 ctx：父取消会传播到 askCtx，而审批自身的超时
		// 也能在轮次之间被及时发现。
		if err := askCtx.Err(); err != nil {
			return err
		}
		// 二次确认阶段把待记住的作用域摘要交给审批器展示；其余阶段传空串。
		summary := ""
		if stage == stageConfirmScope {
			summary = ScopeSummary(scope)
		}
		decision, err := staged.AskStage(askCtx, name, prompt, stage, summary)
		if err != nil {
			// 与原始审批失败一致：不可用通道记审计，取消不记；两者都返回不带
			// 终结语句的包装错误，因为没有人给出最终回答。
			if ClassifyApproval(false, err) == ApprovalUnavailable {
				c.record(name, "unavailable", "the approval channel is unavailable", auditArgs)
			}
			return fmt.Errorf("security: approval for the %s tool call failed: %w", name, err)
		}

		// 暂存进入子阶段时携带的数据。AdvanceStage 是纯函数，不会改 decision。
		switch stage {
		case stageOnce:
			scope = append([]string(nil), decision.Scope...)
			feedback = decision.Feedback
		case stageConfirmScope:
			// 允许二次确认时携带更精确的作用域覆盖初次选择。
			if len(decision.Scope) > 0 {
				scope = append([]string(nil), decision.Scope...)
			}
		case stageFeedback:
			// 反馈可能分多轮收集，取最新的非空文本。
			if strings.TrimSpace(decision.Feedback) != "" {
				feedback = decision.Feedback
			}
		}

		nextStage, completed := AdvanceStage(stage, decision)
		if !completed {
			stage = nextStage
			continue
		}

		switch stage {
		case stageConfirmScope:
			// always 只有在二次确认通过后才真正记住作用域。
			staged.RememberScope(append([]string(nil), scope...))
			c.record(name, "allow", "approved by the operator (always)", auditArgs)
			return nil
		case stageFeedback:
			if text := strings.TrimSpace(feedback); text != "" {
				c.record(name, "refused", "the operator rejected the tool call with feedback", auditArgs)
				return &RefusalError{Tool: name, Feedback: clipApprovalFeedback(text)}
			}
			c.record(name, "refused", "the operator refused the tool call", auditArgs)
			return &StopError{Tool: name, Reason: "the operator refused the tool call", Refused: true}
		default:
			// stageOnce 上只有 ChoiceOnce 会完成：仅此一次放行，不记忆作用域。
			c.record(name, "allow", "approved by the operator", auditArgs)
			return nil
		}
	}

	// 超过交互上限：失败即拒绝，绝不因异常通道而放行。
	c.record(name, "deny", "staged approval did not complete", auditArgs)
	return &StopError{Tool: name, Reason: "staged approval did not complete"}
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
