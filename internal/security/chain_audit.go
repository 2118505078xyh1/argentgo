package security

import (
	"strings"
	"sync"
)

// chain_audit.go 提供一个可选的"链执行轨迹"记录器。
//
// 它刻意不接入 Chain.check：当前的安全链行为保持不变。后续接线时，
// 可以在策略判定、审批回答、审计写入各点调用这里的记录方法，再用
// InvariantOK 断言"策略 → 审批 → 审计"的顺序没有被绕过。
// 本文件只做内存中的状态记录，不执行任何 I/O。

// AuditTrace 记录一次工具调用走过的安全链步骤。
//
// 字段与 Chain.check 的分支一一对应：
//   - PolicyDecision：策略判定，取值遵循 Decision.String()（allow / ask / deny）；
//   - ApprovalAsked / ApprovalGranted：审批是否被询问，以及得到的回答；
//   - Audited：是否已写入审计记录；
//   - Redacted：写入审计前是否已完成脱敏。
//
// mu 只保证并发读写安全，不参与不变量判定。零值可直接使用。
type AuditTrace struct {
	PolicyDecision  string
	ApprovalAsked   bool
	ApprovalGranted bool
	Audited         bool
	Redacted        bool

	mu sync.RWMutex
}

// NewAuditTrace 返回一个空轨迹。它不触碰任何外部资源。
func NewAuditTrace() *AuditTrace { return &AuditTrace{} }

// SetPolicy 记录策略判定。nil 接收者是合法的无记录器状态，直接忽略。
func (t *AuditTrace) SetPolicy(decision string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.PolicyDecision = decision
}

// SetApproval 记录审批是否被询问及其回答。
// ApprovalAsked 置位表示审批器已经给出答案；granted 为 true 是同意，
// 为 false 则是否决（不可用/取消的通道在链里同样按否决处理）。
func (t *AuditTrace) SetApproval(asked, granted bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ApprovalAsked = asked
	t.ApprovalGranted = granted
}

// SetAudited 记录审计已写入。
func (t *AuditTrace) SetAudited() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Audited = true
}

// SetRedacted 记录写入审计前已完成脱敏。
func (t *AuditTrace) SetRedacted() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Redacted = true
}

// InvariantOK 检查链不变量。它是纯函数：只读、无副作用、无 I/O，
// 判定只依赖 ASCII 判定词，与 CJK 文本无关。
//
//  1. 政策拒绝时，审批不得被询问，但必须已写审计——拒绝在审批之前
//     短路，却仍要留下记录；
//  2. 审批被问时，答案必须继续走到审计才算落定，否则是半途状态；
//  3. 已写审计时，必须先脱敏，防止原始参数进入审计日志。
//
// nil 接收者表示没有记录器，没有可违反的步骤，返回 true。
func (t *AuditTrace) InvariantOK() bool {
	if t == nil {
		return true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	if isTraceDeny(t.PolicyDecision) && (t.ApprovalAsked || !t.Audited) {
		return false
	}
	if t.ApprovalAsked && !t.Audited {
		return false
	}
	if t.Audited && !t.Redacted {
		return false
	}
	return true
}

// isTraceDeny 只比较 ASCII 判定词，不包含任何 CJK 字面量，
// 因此策略原因里出现中文不会影响不变量判定。
func isTraceDeny(decision string) bool {
	return strings.EqualFold(strings.TrimSpace(decision), "deny")
}
