package app

import (
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// 本文件为两个已经位于无依赖核心包中的纯辅助函数提供附加性的接线适配器。
// 它们刻意放在 app 包（唯一允许导入所有包的包）中，使核心包不会增加任何 app 层依赖。
//
// 在各自功能未被使用的路径上，每个适配器都是空操作：没有声明的提示不会改动 Budget，
// 干净的记录会产生空通知且不需要确认。这使它们可以在最终的 Execute 调用点上无条件调用，
// 而不改变现有运行。

// applyDeclaredTurnBudget 把用户在提示中声明的单轮 token 预算复制到 b.DeclaredTurnTokens。
// 解析委托给 agent.ParseTurnBudget，它会忽略代码段、注入的 XML 块和 @-引用，
// 因此只有用户本人的正文才能设置上限。
//
// 当 b 为 nil 或提示没有声明时它是空操作，因此调用方可以在每次运行都调用它，
// 而不改变未声明任何内容的运行的行为。有声明时它会覆盖任何先前的值；
// 解析出的上限是一个额外的累计 token 限制，绝不会放宽 b 上配置的其他限制。
func applyDeclaredTurnBudget(prompt string, b *agent.Budget) {
	if b == nil {
		return
	}
	tokens, ok := agent.ParseTurnBudget(prompt)
	if !ok {
		return
	}
	b.DeclaredTurnTokens = tokens
}

// recoveryDecision 是 app 层对 session.ClassifyRecovery 的视图：
// 要宣告的用户可见通知，以及继续运行是否需要操作人员显式决定。
// 从同一个计划计算出两者，可防止未来的接线点对同一记录做两次分类。
//
// 细节字段（Category / OrphanToolCalls / OrphanToolResults / BrokenLinks）是同一份
// RecoveryPlan 的只读投影：它们在 recoveryDecisionFor 里一次性填充，供 opt-in 的
// 细节后缀与测试消费，绝不再对同一记录做第二次分类。字段本身不改写 Notice；
// 是否把细节作为文本追加到公告完全由 Execute 的 ARGENT_RECOVERY_DETAIL 门控决定。
type recoveryDecision struct {
	Notice            string
	NeedsConfirmation bool

	Category          string
	OrphanToolCalls   []string
	OrphanToolResults []string
	BrokenLinks       int
}

// recoveryDecisionFor 对消息分类一次，同时返回通知、确认要求和计划细节。
// 它对应 session.ClassifyRecovery 的保守规则：只有降级的历史才使 NeedsConfirmation 为 true，
// 而被中断的提示/轮次可自动继续，但仍会被宣告。
func recoveryDecisionFor(messages []session.Entry) recoveryDecision {
	plan := session.ClassifyRecovery(messages)
	return recoveryDecision{
		Notice:            plan.Announcement,
		NeedsConfirmation: !plan.CanAutoContinue || plan.RequiresUserConfirmation,
		Category:          string(plan.Category),
		OrphanToolCalls:   plan.OrphanToolCalls,
		OrphanToolResults: plan.OrphanToolResults,
		BrokenLinks:       plan.BrokenLinks,
	}
}

// boundedDetailIDs 把一组 id 拼接为有界、确定性的展示串。
// 复用 replay_suffix.go 的 boundedReplayID（按 rune 截断，CJK 不会被切坏），
// 使诊断后缀的长度有上界且任意 UTF-8 输入都保持合法。
func boundedDetailIDs(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, boundedReplayID(id))
	}
	return strings.Join(parts, ",")
}

// recoveryDetailSuffix 把 recoveryDecision 中一次性分类得到的细节投影成一段
// 确定性的中文诊断后缀，供 Execute 的恢复路径 opt-in 追加到公告末尾。
//
// 它是纯函数：只读入参、不改 decision 及其切片，同一入参逐字节稳定。
// 它只做加法——绝不修改 NeedsConfirmation，也不把合成的中止结果当作真实应答。
// 干净的记录由调用方以 Notice 为空短路，不会走到这里，因此不会产生凭空公告。
func recoveryDetailSuffix(decision recoveryDecision) string {
	var b strings.Builder
	b.WriteString(" (恢复细节：类别=")
	b.WriteString(decision.Category)
	b.WriteString("，断链=")
	b.WriteString(strconv.Itoa(decision.BrokenLinks))
	b.WriteString("，孤儿调用=")
	b.WriteString(strconv.Itoa(len(decision.OrphanToolCalls)))
	b.WriteString("，孤儿结果=")
	b.WriteString(strconv.Itoa(len(decision.OrphanToolResults)))
	if len(decision.OrphanToolCalls) > 0 {
		b.WriteString("，孤儿调用ID=")
		b.WriteString(boundedDetailIDs(decision.OrphanToolCalls))
	}
	if len(decision.OrphanToolResults) > 0 {
		b.WriteString("，孤儿结果ID=")
		b.WriteString(boundedDetailIDs(decision.OrphanToolResults))
	}
	b.WriteString(")")
	return b.String()
}

// recoveryNotice 返回恢复消息时用户可见的宣告。干净的记录返回空字符串。
// 它保留为仅返回字符串的适配器，使 app 层无需依赖 RecoveryPlan 的结构即可呈现通知。
func recoveryNotice(messages []session.Entry) string {
	return recoveryDecisionFor(messages).Notice
}

// recoveryNeedsConfirmation 报告在继续运行之前，恢复消息是否需要操作人员的显式决定。
func recoveryNeedsConfirmation(messages []session.Entry) bool {
	return recoveryDecisionFor(messages).NeedsConfirmation
}
