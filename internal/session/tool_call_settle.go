package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SettleReason 描述一条工具调用为什么没有得到真实结果。
// 它作为结构化元数据写入合成结果的 Data，便于恢复逻辑与前端据此选择公告文案。
type SettleReason string

const (
	// SettleInterrupted 表示上一轮被执行中断（取消/截断/崩溃）而没有完成工具。
	SettleInterrupted SettleReason = "interrupted"
	// SettleCanceled 表示工具调用被显式取消。
	SettleCanceled SettleReason = "canceled"

	// settleReasonKey 是合成结果 Data 中携带原因的键名。
	settleReasonKey = "reason"

	// maxSettledResults 限制单次结算插入/富化的合成结果数量，
	// 防止损坏的转录让投影结果无限膨胀。与 ReconcileToolPairs 的上限保持一致。
	maxSettledResults = 100
)

// settleResultID 从工具调用 id 派生一个稳定的短 id（sha256 的前 12 个十六进制字符）。
// 同一调用每次重放都会得到相同 id，让发给 provider 的消息前缀保持稳定，
// 从而不破坏 prompt cache。前缀长度固定为 12，且只含十六进制字符，
// 不会与追加路径生成的 n%06d 记录 id 冲突。
func settleResultID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:])[:12]
}

// SettleOrphanToolCalls 对投影对话做工具调用结算：找出没有配对结果的 assistant 调用，
// 并在它后面插入一条带原因的合成结果；对已经由 ReconcileToolPairs 插入的中止结果，
// 则就地补齐原因元数据与稳定的短 id，避免重复插入。
//
// 这是纯函数：输入切片及其条目永不被修改，干净会话（没有孤儿调用、也没有合成结果）
// 原样返回输入切片，因此逐字节不变。函数幂等：对同一输入运行两次得到相同结果。
//
// reason 为空时按 SettleInterrupted 处理。合成结果的 Content 刻意保持为
// abortedResultText，使 isSyntheticAborted/ClassifyRecovery 仍能识别它是内部记账信息；
// 原因以结构化字段写入 Data，随导入/导出一起保留。
func SettleOrphanToolCalls(entries []Entry, reason SettleReason) []Entry {
	if len(entries) == 0 {
		return entries
	}
	if reason == "" {
		reason = SettleInterrupted
	}

	// 第 1 轮：按首次出现顺序收集 assistant 调用 id，并收集已有结果 id。
	calls := make([]string, 0)
	callSet := make(map[string]struct{})
	results := make(map[string]struct{})
	syntheticCalls := make(map[string]struct{})
	for _, entry := range entries {
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				if id == "" {
					continue
				}
				if _, ok := callSet[id]; !ok {
					callSet[id] = struct{}{}
					calls = append(calls, id)
				}
			}
		case "tool":
			if entry.ToolCallID != "" {
				results[entry.ToolCallID] = struct{}{}
			}
			// 已存在的中止结果标记为其调用的配对项，稍后就地富化而不是重复插入。
			if isSyntheticAborted(entry) {
				if _, ok := callSet[entry.ToolCallID]; ok {
					syntheticCalls[entry.ToolCallID] = struct{}{}
				}
			}
		}
	}
	if len(calls) == 0 {
		return entries
	}

	// 第 2 轮：确定真正需要新合成结果的孤儿调用。已有中止结果的调用不需要再插入。
	orphans := make(map[string]struct{})
	for _, id := range calls {
		if _, ok := results[id]; ok {
			continue
		}
		if _, ok := syntheticCalls[id]; ok {
			continue
		}
		orphans[id] = struct{}{}
	}
	if len(orphans) == 0 && len(syntheticCalls) == 0 {
		return entries
	}

	// 第 3 轮：复制条目，就地富化已有中止结果，并在声明孤儿调用的 assistant 之后
	// 插入合成结果。删除 orphans 中的 id 保证每个调用最多插入一次。
	out := make([]Entry, 0, len(entries)+len(orphans))
	inserted := 0
	for _, entry := range entries {
		if isSyntheticAborted(entry) && entry.ToolCallID != "" {
			if _, ok := syntheticCalls[entry.ToolCallID]; ok {
				entry = enrichSettledResult(entry, reason)
			}
		}
		out = append(out, entry)
		if entry.Role != "assistant" {
			continue
		}
		for _, id := range toolCallIDs(entry) {
			if _, ok := orphans[id]; !ok {
				continue
			}
			if inserted >= maxSettledResults {
				continue
			}
			delete(orphans, id)
			inserted++
			out = append(out, newSettledResult(id, reason))
		}
	}
	return out
}

// newSettledResult 为孤儿调用构建一条带原因的合成工具结果。
func newSettledResult(callID string, reason SettleReason) Entry {
	return Entry{
		ID:         settleResultID(callID),
		Role:       "tool",
		Content:    abortedResultText,
		ToolCallID: callID,
		Data:       settledResultData(callID, reason),
	}
}

// enrichSettledResult 把已有中止结果的 id 换成稳定短 id 并写入原因元数据。
// Content 刻意不变，保持 isSyntheticAborted 的识别能力。
func enrichSettledResult(entry Entry, reason SettleReason) Entry {
	entry.ID = settleResultID(entry.ToolCallID)
	entry.Data = settledResultData(entry.ToolCallID, reason)
	return entry
}

// settledResultData 构建合成结果的结构化载荷：原因键让调用方可读，
// 其余键让导入/回读路径能在条目没有原始 Data 时重建等价的投影。
func settledResultData(callID string, reason SettleReason) json.RawMessage {
	data, err := json.Marshal(map[string]any{
		"role":          "tool",
		"content":       abortedResultText,
		"tool_call_id":  callID,
		settleReasonKey: string(reason),
	})
	if err != nil {
		// map[string]any 仅含字符串，不可能 marshal 失败；保险起见返回 null。
		return json.RawMessage("null")
	}
	return data
}
