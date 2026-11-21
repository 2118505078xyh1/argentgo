package session

import (
	"errors"
	"fmt"
)

// replay_state.go 提供确定性的会话状态重放原语。
//
// 这里把一段记录流折叠成不可变的状态快照：活跃叶子、记录数、未配对的
// assistant 工具调用 id、最后一条记录的 kind，以及整体完整性标志。
// 所有函数都是纯函数：只读入参，不写文件，不调用模型，也不修改 records
// 及其 Data；返回的切片一律重新分配底层数组。它们目前没有调用方，
// 存在意义是给上层提供一个可单独验证的重放/对账基准。

var (
	// ErrUnpairedTool 表示重放得到的未配对工具调用集合与预期不一致。
	ErrUnpairedTool = errors.New("session: unpaired tool call")
	// ErrCycle 表示 parent 链存在环，无法得到确定的叶子路径。
	ErrCycle = errors.New("session: parent cycle")
	// ErrLeafDrift 表示重放得到的活跃叶子与预期不一致。
	ErrLeafDrift = errors.New("session: active leaf drift")
)

// ReplayState 是一段记录流重放后的确定性状态快照。
type ReplayState struct {
	// ActiveLeaf 是文件顺序最后一条记录的 id，与 Branches 的 Active 标记一致。
	// 空流时为空字符串。
	ActiveLeaf string
	// RecordCount 是记录条数。
	RecordCount int
	// UnpairedToolCalls 是未配对的工具调用 id，按首次出现顺序排列、去重。
	// 它始终是非 nil 切片；无未配对调用时为空切片。
	UnpairedToolCalls []string
	// LastKind 是最后一条记录的 kind；空流时为空字符串。
	LastKind string
	// Complete 报告流既没有未配对的工具调用，parent 链也没有环。
	// 空流视为完整。
	Complete bool
}

// Replay 按文件顺序折叠 records，返回确定性的状态快照。
//
// 活跃叶子沿用现有恢复逻辑：取文件顺序最后一条记录的 id；随后沿 parent
// 链向上走以检测环，遇到已访问 id 即判定有环并停止（不会死循环），
// parent 指向不存在的 id 时按现有读取路径的处理方式停止，不算环。
// 工具调用配对读取整段流（与分支无关）：assistant 记录中声明的每个
// tool_calls/toolCalls id，若没有任何 tool 记录携带相同的 ToolCallID，
// 就记为未配对。Complete 仅在既无未配对调用又无环时为真。
// 入参 records 及其 Data 均不被修改。
func Replay(records []Record) ReplayState {
	state, _ := replay(records)
	return state
}

// replay 是 Replay 与 VerifyReplay 共用的内部实现，额外返回是否检测到环，
// 使 VerifyReplay 能把环与未配对调用区分成不同的哨兵错误。
func replay(records []Record) (ReplayState, bool) {
	state := ReplayState{
		RecordCount:       len(records),
		UnpairedToolCalls: make([]string, 0),
	}
	if len(records) == 0 {
		state.Complete = true
		return state, false
	}

	// 与 Branches/activePath 一致：活跃叶子就是最后一条记录。
	state.ActiveLeaf = records[len(records)-1].ID
	state.LastKind = records[len(records)-1].Kind

	byID := indexByID(records)
	cycle := false
	seen := make(map[string]bool, len(records))
	for current := state.ActiveLeaf; current != ""; {
		if seen[current] {
			cycle = true
			break
		}
		seen[current] = true
		record, ok := byID[current]
		if !ok {
			break
		}
		current = record.Parent
	}

	// 收集 assistant 声明的调用 id（首次出现顺序）与所有工具结果 id，
	// 二者都按整段流统计，结果可以出现在调用之前。
	calls := make([]string, 0)
	callSeen := make(map[string]bool)
	results := make(map[string]bool)
	for _, record := range records {
		entry, ok := messageEntry(record)
		if !ok {
			continue
		}
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				if id == "" || callSeen[id] {
					continue
				}
				callSeen[id] = true
				calls = append(calls, id)
			}
		case "tool":
			if entry.ToolCallID != "" {
				results[entry.ToolCallID] = true
			}
		}
	}
	for _, id := range calls {
		if !results[id] {
			state.UnpairedToolCalls = append(state.UnpairedToolCalls, id)
		}
	}

	state.Complete = !cycle && len(state.UnpairedToolCalls) == 0
	return state, cycle
}

// VerifyReplay 对 records 重放，并把结果与 expected 逐字段比对。
//
// 它只报告三类确定性偏差，分别用哨兵错误标记，便于调用方用 errors.Is 分类：
//   - parent 链存在环 → ErrCycle；
//   - 未配对工具调用集合与预期不同 → ErrUnpairedTool；
//   - 活跃叶子与预期不同 → ErrLeafDrift。
//
// 检查顺序如上：环最根本，会先于其他偏差返回。其余字段（RecordCount、
// LastKind、Complete）的偏差用普通错误报告。入参不被修改。
func VerifyReplay(records []Record, expected ReplayState) error {
	got, cycle := replay(records)
	if cycle {
		return fmt.Errorf("%w: leaf %q", ErrCycle, got.ActiveLeaf)
	}
	if !sameStrings(got.UnpairedToolCalls, expected.UnpairedToolCalls) {
		return fmt.Errorf("%w: got %v, expected %v", ErrUnpairedTool, got.UnpairedToolCalls, expected.UnpairedToolCalls)
	}
	if got.ActiveLeaf != expected.ActiveLeaf {
		return fmt.Errorf("%w: got %q, expected %q", ErrLeafDrift, got.ActiveLeaf, expected.ActiveLeaf)
	}
	if got.RecordCount != expected.RecordCount {
		return fmt.Errorf("session: replay record count %d, expected %d", got.RecordCount, expected.RecordCount)
	}
	if got.LastKind != expected.LastKind {
		return fmt.Errorf("session: replay last kind %q, expected %q", got.LastKind, expected.LastKind)
	}
	if got.Complete != expected.Complete {
		return fmt.Errorf("session: replay complete %t, expected %t", got.Complete, expected.Complete)
	}
	return nil
}

// sameStrings 比较两个字符串切片是否逐项相同；nil 与空切片视为相等。
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
