package session

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	// abortedResultText 是在重放时从未产生输出的工具调用插入的合成工具结果的内容。
	abortedResultText = "[aborted] tool did not run"

	// abortedIDPrefix 为确定性的合成 id 提供命名空间，使其不会与为真实记录生成的 id 冲突。
	abortedIDPrefix = "session:aborted-tool-result"

	// maxSyntheticResults 限制单次重放可以插入多少条中止结果，
	// 防止损坏的转录使投影结果膨胀。
	maxSyntheticResults = 100
)

// abortedResultID 从工具调用 id 派生一个稳定的合成结果 id。
// 对同一会话重放两次会得到相同的 id，这很重要，因为这些 id 会作为真实工具调用的配对项传给模型。
func abortedResultID(callID string) string {
	sum := sha256.Sum256([]byte(abortedIDPrefix + ":" + callID))
	return hex.EncodeToString(sum[:])
}

// ReconcileToolPairs 修复投影对话中工具调用/结果的配对。它丢弃调用不存在的结果条目，
// 并为每个没有结果的调用插入一条合成的中止结果，与恢复 JSONL 会话时执行的重放期协调一致。
//
// 输入永远不会被修改：现有条目按值复制到新的切片，合成结果追加到该副本上。
// 没有任何工具调用元数据的条目原样通过（这种情况下直接返回输入切片），
// 因此干净的会话保持逐字节相同的投影。
func ReconcileToolPairs(entries []Entry) []Entry {
	if len(entries) == 0 {
		return entries
	}

	// 第 1 轮：收集 assistant 调用和工具结果声明的 id。
	calls := make(map[string]struct{})
	results := make(map[string]struct{})
	for _, entry := range entries {
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				calls[id] = struct{}{}
			}
		case "tool":
			if entry.ToolCallID != "" {
				results[entry.ToolCallID] = struct{}{}
			}
		}
	}
	if len(calls) == 0 && len(results) == 0 {
		// 完全没有工具元数据：无需协调。
		return entries
	}

	// 第 2 轮：保留调用存在的结果，为没有结果的调用合成结果。合成结果紧跟在声明该调用的
	// assistant 条目之后（按声明顺序），且每个 id 最多插入一次。
	out := make([]Entry, 0, len(entries)+len(calls))
	synthesized := make(map[string]struct{})
	inserted := 0
	for _, entry := range entries {
		if entry.Role == "tool" && entry.ToolCallID != "" {
			if _, ok := calls[entry.ToolCallID]; !ok {
				// 孤立结果：它所响应的调用不在转录中。
				continue
			}
		}
		out = append(out, entry)

		if entry.Role != "assistant" {
			continue
		}
		for _, id := range toolCallIDs(entry) {
			if _, ok := results[id]; ok {
				continue
			}
			if _, ok := synthesized[id]; ok {
				continue
			}
			if inserted >= maxSyntheticResults {
				continue
			}
			synthesized[id] = struct{}{}
			inserted++
			out = append(out, Entry{
				ID:         abortedResultID(id),
				Role:       "tool",
				Content:    abortedResultText,
				ToolCallID: id,
			})
		}
	}
	return out
}

// RecoveryCategory 表示恢复会话时发现转录所处的状态。四个值按所需的人工介入程度排序：
// clean 无需介入，degraded_history 需要人工决策。
type RecoveryCategory string

const (
	RecoveryClean             RecoveryCategory = "clean"
	RecoveryInterruptedPrompt RecoveryCategory = "interrupted_prompt"
	RecoveryInterruptedTurn   RecoveryCategory = "interrupted_turn"
	RecoveryDegradedHistory   RecoveryCategory = "degraded_history"
)

// RecoveryPlan 携带的可见公告。它们是普通且稳定的字符串，
// 因此这个仅依赖标准库的包不需要 i18n 依赖；前端可以自由地根据 Category 进行本地化。
const (
	announceInterruptedPrompt = "Session recovery: the last user prompt had no reply before the run stopped; it will be answered on resume."
	announceInterruptedTurn   = "Session recovery: the previous turn stopped mid-execution; unanswered tool calls will be answered with an aborted result before continuing."
	announceDegradedHistory   = "Session recovery: history is degraded (broken record link or orphaned tool result); automatic continuation is disabled — please confirm before resuming."
)

// RecoveryPlan 是对恢复对话时所在状态的结构化只读描述。它刻意不做任何更改：
// 工具调用/结果的配对仍由 ReconcileToolPairs 在重放时修复，物理损坏仍由 ValidateAndRepair 处理。
// 调用方只使用该计划来决定是否自动继续、是否询问用户以及公告什么内容。
//
// 详情字段已去重并保持首次出现的顺序，因此从同一转录得出结论的两个调用方看到相同的值。
type RecoveryPlan struct {
	Category RecoveryCategory

	// CanAutoContinue 报告运行是否可以在无需新的人工决策的情况下继续。
	// 仅当状态为 degraded_history 时为 false。
	CanAutoContinue bool
	// RequiresUserConfirmation 报告是否必须由操作员显式批准才能继续。
	// 仅当状态为 degraded_history 时为 true。
	RequiresUserConfirmation bool

	// Announcement 是该计划对用户可见的通知。对于干净的会话，它为空，因为没有什么可报告的。
	Announcement string

	// OrphanToolCalls 列出没有真实结果的 assistant 工具调用，因此会被 ReconcileToolPairs
	// 用合成的中止结果来应答。输入中已存在的合成结果不算作应答，
	// 因此重放后的转录与原始转录的分类结果相同。
	OrphanToolCalls []string
	// OrphanToolResults 列出调用在转录中缺失的工具结果；与缺失结果不同，缺失调用意味着历史丢失。
	OrphanToolResults []string
	// BrokenLinks 统计没有指向线性路径上一条消息的 parent 链接数量。
	BrokenLinks int
	// 当转录以未得到应答的用户提示结束时，PendingPrompt 为 true。
	PendingPrompt bool
	// 当转录在 assistant 轮次中途结束时（悬空调用或没有最终应答的结果），InterruptedAssistant 为 true。
	InterruptedAssistant bool
}

// ClassifyRecovery 检查投影对话并返回用于恢复它的计划。它是纯函数：输入切片及其条目永远不会被修改。
// 分类是保守的——无法信任的历史会被报告为 degraded_history，从而禁用自动继续。
//
// 判定顺序，从最严重开始：
//   - parent 链接断开或存在孤立的工具结果 => degraded_history；
//   - 末尾有未应答的用户提示              => interrupted_prompt；
//   - 末尾有工具结果或未应答的调用        => interrupted_turn；
//   - 其他情况                            => clean。
//
// ReconcileToolPairs 生成的合成中止结果在检查 parent 链接时会被跳过
// （它们自身不携带 parent），也从不算作真实应答，因此对重放后的转录进行分类
// 等价于对重放前的原始历史进行分类。
func ClassifyRecovery(messages []Entry) RecoveryPlan {
	plan := RecoveryPlan{Category: RecoveryClean, CanAutoContinue: true}
	if len(messages) == 0 {
		return plan
	}

	// 为声明和真实应答建立索引。合成的中止结果不是应答：它本身就是恢复机制，
	// 而不是模型产生的东西。对声明去重，同时保持首次出现的顺序。
	calls := make(map[string]struct{})
	callOrder := make([]string, 0)
	results := make(map[string]struct{})
	for _, entry := range messages {
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				if id == "" {
					continue
				}
				if _, ok := calls[id]; !ok {
					calls[id] = struct{}{}
					callOrder = append(callOrder, id)
				}
			}
		case "tool":
			if entry.ToolCallID == "" || isSyntheticAborted(entry) {
				continue
			}
			results[entry.ToolCallID] = struct{}{}
		}
	}

	for _, id := range callOrder {
		if _, ok := results[id]; !ok {
			plan.OrphanToolCalls = append(plan.OrphanToolCalls, id)
		}
	}
	seenOrphanResult := make(map[string]struct{})
	for _, entry := range messages {
		if entry.Role != "tool" || isSyntheticAborted(entry) || entry.ToolCallID == "" {
			continue
		}
		if _, ok := calls[entry.ToolCallID]; ok {
			continue
		}
		if _, ok := seenOrphanResult[entry.ToolCallID]; ok {
			continue
		}
		seenOrphanResult[entry.ToolCallID] = struct{}{}
		plan.OrphanToolResults = append(plan.OrphanToolResults, entry.ToolCallID)
	}

	plan.BrokenLinks = countBrokenLinks(messages)
	if plan.BrokenLinks > 0 || len(plan.OrphanToolResults) > 0 {
		plan.Category = RecoveryDegradedHistory
		plan.CanAutoContinue = false
		plan.RequiresUserConfirmation = true
		plan.Announcement = announceDegradedHistory
		return plan
	}

	last := lastRealEntry(messages)
	if last == nil {
		return plan
	}
	switch {
	case last.Role == "user":
		plan.PendingPrompt = true
		plan.Category = RecoveryInterruptedPrompt
		plan.Announcement = announceInterruptedPrompt
	case last.Role == "tool" || len(plan.OrphanToolCalls) > 0:
		plan.InterruptedAssistant = true
		plan.Category = RecoveryInterruptedTurn
		plan.Announcement = announceInterruptedTurn
	}
	return plan
}

// isSyntheticAborted 识别 ReconcileToolPairs 在重放时插入的中止结果。
// 它们是内部记账信息，不是用户可见的历史。
func isSyntheticAborted(entry Entry) bool {
	return entry.Role == "tool" && entry.Content == abortedResultText
}

// lastRealEntry 返回最新的非合成条目；如果对话仅由合成结果组成，则返回 nil。
func lastRealEntry(messages []Entry) *Entry {
	for i := len(messages) - 1; i >= 0; i-- {
		if isSyntheticAborted(messages[i]) {
			continue
		}
		return &messages[i]
	}
	return nil
}

// countBrokenLinks 检查每条消息的 parent id 是否为线性路径上一条消息的 id。
// 第一个条目是边界（其 parent 可能是 header 或已被压缩掉的记录），会被跳过；
// 紧跟在系统压缩摘要之后的条目同样跳过。id 或 parent 为空的条目无法提供任何证据，
// 因此跳过而不是猜测。合成结果被完全跳过，使其前面的真实消息保持为链接目标。
//
// 压缩摘要在投影中代表压缩记录本身：压缩时因保留标记而被重新发出的记录，其 parent
// 指向这条压缩记录（即摘要条目的 id），而不是投影中紧邻的前一条消息。这类链接是合法的，
// 不能计入断链，否则任何带有重放记录的会话在恢复时都会被误判为 degraded_history，
// 从而禁用自动继续。因此指向任一系统摘要条目的 parent 都视为有效链接目标。
func countBrokenLinks(messages []Entry) int {
	broken := 0
	// 收集系统摘要条目的 id，作为重放记录的合法父节点集合。
	summaries := make(map[string]bool)
	for i := range messages {
		if messages[i].Role == "system" && messages[i].ID != "" {
			summaries[messages[i].ID] = true
		}
	}
	var prev *Entry
	for i := range messages {
		entry := &messages[i]
		if isSyntheticAborted(*entry) {
			continue
		}
		if prev != nil && prev.Role != "system" &&
			entry.Parent != "" && prev.ID != "" && entry.Parent != prev.ID &&
			!summaries[entry.Parent] {
			broken++
		}
		prev = entry
	}
	return broken
}
