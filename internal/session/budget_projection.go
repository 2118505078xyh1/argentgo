package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// 本文件提供一个纯粹的、与压缩路径解耦的"预算投影"原语：
// Project 把一段线性对话投影到给定的 rune 预算内，同时尽量保持工具调用/结果配对的完整性、
// 保护最新的用户输入与未配对的活尾工具调用。它不修改入参、不触碰会话文件、不接线到任何默认压缩流程。

// ProjectionActionKind 是投影动作的稳定分类码。
type ProjectionActionKind string

const (
	// ProjectionDropPair 表示按工具配对边界整对丢弃（assistant 工具调用连同其全部结果）。
	ProjectionDropPair ProjectionActionKind = "drop_pair"
	// ProjectionDropMessage 表示丢弃一条不属于任何配对的普通消息。
	ProjectionDropMessage ProjectionActionKind = "drop_message"
	// ProjectionDropThinking 表示清除 assistant 的思考文本（Reasoning/签名）。
	ProjectionDropThinking ProjectionActionKind = "drop_thinking"
	// ProjectionTruncate 表示对消息内容做了 rune 安全截断。
	ProjectionTruncate ProjectionActionKind = "truncate"
)

// ProjectionAction 描述 Project 执行的一项可见操作，供调用方审计或解释结果。
// Index 始终是消息在 *输入* 切片中的下标；Detail 是人类可读的中文说明。
type ProjectionAction struct {
	Kind   ProjectionActionKind
	Index  int
	Detail string
}

// ProjectionWarningCode 是投影告警的稳定分类码。
type ProjectionWarningCode string

const (
	// ProjectionBudgetNotPositive 表示预算不是正数，无法进行有意义的投影。
	ProjectionBudgetNotPositive ProjectionWarningCode = "budget_not_positive"
	// ProjectionBudgetUnreachable 表示预算小到必须截断受保护消息才能容纳，
	// 即"完整保留受保护上下文"这一要求不可达。
	ProjectionBudgetUnreachable ProjectionWarningCode = "budget_unreachable"
)

// ProjectionWarning 是对无法完全满足的约束的显式告警。Project 遇到不可达预算时
// 绝不 panic、也绝不静默返回：它总是尽力产出结果并附上告警。
type ProjectionWarning struct {
	Code    ProjectionWarningCode
	Message string
}

// ProjectionPolicy 控制 Project 的保留策略。零值表示不启用任何特殊保护与清理。
type ProjectionPolicy struct {
	// KeepRecentUser 保护最新一条 user 消息不被丢弃（必要时仍可能被 rune 安全截断）。
	KeepRecentUser bool
	// KeepLiveToolTail 保护末尾那些声明了"没有对应结果"的工具调用的 assistant 消息，
	// 即仍处于执行中的活尾 tool_use。
	KeepLiveToolTail bool
	// DropThinking 清除保留下来的 assistant 消息的 Reasoning/ReasoningSignature。
	DropThinking bool
}

// ProjectionResult 是 Project 的投影结果。
type ProjectionResult struct {
	// Messages 是按原顺序排列的保留消息；它们是值拷贝，修改它们不会影响入参。
	Messages []Entry
	// Runes 是 Messages 中内容字段实际占用的 rune 总数（截断后重新统计）。
	Runes int
	// Budget 是本次投影使用的预算，原样回显，便于调用方核对。
	Budget int
	// Truncated 报告是否发生过 rune 安全截断。
	Truncated bool
}

// projectionPlan 把消息下标划分为若干"必须同留同弃"的单元。
// 单元由工具调用 id 连接而成：声明同一 id 的 assistant（重试）以及响应它的 tool 结果
// 被并入同一连通分量，从而保证切点永远不会拆开一对调用/结果。
type projectionPlan struct {
	unitOf []int   // 消息下标 -> 单元编号
	units  [][]int // 单元编号 -> 升序消息下标
	order  []int   // 单元编号，按各单元最小下标升序排列（ oldest -> newest ）
}

// Project 把 messages 投影到 budget 个内容 rune 以内。
//
// 预算口径：只统计每条消息 Content 的 rune 数（Reasoning 属转录元数据，不计入预算）；
// 这样 CJK 与 emoji 这类多字节字符按"一个字符一个 rune"计费，而非按字节。
//
// 行为：
//   - 切分只在消息边界进行，且工具调用与其结果按配对边界整对保留/丢弃；
//   - 切点不会落在"孤立的 tool_result"上（这类无调用结果会被直接丢弃）；
//   - KeepRecentUser / KeepLiveToolTail 指定的消息即使超出预算也不会被丢弃；
//   - 当丢弃后仍然超限，会从最老的消息开始做 rune 安全截断，并按 ProjectionTruncate 标记；
//   - 预算非正、或必须截断受保护消息才能容纳时，返回明确的告警，不 panic、不静默。
//
// 入参绝不被修改；同样的输入与策略一定得到同样的输出（确定性）。
func Project(messages []Entry, budget int, policy ProjectionPolicy) (ProjectionResult, []ProjectionAction, []ProjectionWarning) {
	result := ProjectionResult{Budget: budget, Messages: []Entry{}}
	actions := make([]ProjectionAction, 0)
	warnings := make([]ProjectionWarning, 0)

	n := len(messages)
	if n == 0 {
		return result, actions, warnings
	}

	plan := buildProjectionPlan(messages)
	protected := projectionProtected(messages, plan, policy)

	// 从最新单元向最老单元贪心保留：优先保留最近的上下文。一旦因预算丢弃了普通单元，
	// 更老的普通单元也一并丢弃，避免结果中出现"保留旧消息却丢了新消息"的空洞。
	kept := make([]bool, n)
	total := 0
	exhausted := false
	for oi := len(plan.order) - 1; oi >= 0; oi-- {
		uid := plan.order[oi]
		members := plan.units[uid]
		isProtected := false
		cost := 0
		for _, idx := range members {
			if protected[idx] {
				isProtected = true
			}
			cost += utf8.RuneCountInString(messages[idx].Content)
		}
		if isProtected {
			for _, idx := range members {
				kept[idx] = true
			}
			total += cost
			continue
		}
		if !exhausted && budget > 0 && total+cost <= budget {
			for _, idx := range members {
				kept[idx] = true
			}
			total += cost
		} else {
			exhausted = true
			recordProjectionDrop(&actions, members, messages)
		}
	}

	// 切点不落在孤立 tool_result 上：若保留段最老的消息是没有调用的孤立结果，继续丢弃它。
	for {
		oldest := -1
		for i := 0; i < n; i++ {
			if kept[i] {
				oldest = i
				break
			}
		}
		if oldest < 0 || messages[oldest].Role != "tool" {
			break
		}
		uid := plan.unitOf[oldest]
		if len(plan.units[uid]) != 1 {
			break // 成对单元的成员首条必是 assistant，不会进入这里
		}
		kept[oldest] = false
		total -= utf8.RuneCountInString(messages[oldest].Content)
		recordProjectionDrop(&actions, []int{oldest}, messages)
	}

	// 按原顺序组装结果，逐条值拷贝；同时处理思考文本丢弃。
	keptIndices := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if !kept[i] {
			continue
		}
		entry := messages[i] // 结构体值拷贝；字符串与 Data 只读，不写回
		if policy.DropThinking && entry.Role == "assistant" &&
			(entry.Reasoning != "" || entry.ReasoningSignature != "") {
			entry.Reasoning = ""
			entry.ReasoningSignature = ""
			actions = append(actions, ProjectionAction{
				Kind:   ProjectionDropThinking,
				Index:  i,
				Detail: fmt.Sprintf("丢弃消息 #%d 的思考文本", i),
			})
		}
		keptIndices = append(keptIndices, i)
		result.Messages = append(result.Messages, entry)
	}

	// 超限处理：非正预算直接给出显式告警；正预算则从最老消息开始 rune 安全截断。
	protectedTruncated := false
	if total > budget {
		if budget <= 0 {
			warnings = append(warnings, ProjectionWarning{
				Code:    ProjectionBudgetNotPositive,
				Message: fmt.Sprintf("预算 %d 不是正数，无法完成投影；返回受保护消息的尽力结果", budget),
			})
			warnings = append(warnings, ProjectionWarning{
				Code:    ProjectionBudgetUnreachable,
				Message: "预算不可达：预算必须为正数才能容纳任何消息",
			})
		} else {
			excess := total - budget
			for pos, idx := range keptIndices {
				if excess <= 0 {
					break
				}
				entry := result.Messages[pos]
				runes := utf8.RuneCountInString(entry.Content)
				if runes == 0 {
					continue
				}
				remove := runes
				if remove > excess {
					remove = excess
				}
				entry.Content = truncateProjectionRunes(entry.Content, runes-remove)
				result.Messages[pos] = entry
				result.Truncated = true
				if protected[idx] {
					protectedTruncated = true
				}
				actions = append(actions, ProjectionAction{
					Kind:   ProjectionTruncate,
					Index:  idx,
					Detail: fmt.Sprintf("消息 #%d 内容从 %d rune 安全截断到 %d rune", idx, runes, runes-remove),
				})
				excess -= remove
			}
			if protectedTruncated {
				warnings = append(warnings, ProjectionWarning{
					Code:    ProjectionBudgetUnreachable,
					Message: "预算不可达：必须截断受保护消息才能容纳，完整上下文未能保留",
				})
			}
		}
	}

	// 用最终内容重新统计，截断后口径依然准确。
	result.Runes = 0
	for _, entry := range result.Messages {
		result.Runes += utf8.RuneCountInString(entry.Content)
	}
	return result, actions, warnings
}

// truncateProjectionRunes 按 rune 截断字符串，绝不会把一个多字节字符切成两半。
func truncateProjectionRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// recordProjectionDrop 为被丢弃的单元记录一条动作：单条消息记为 drop_message，
// 配对（多条）记为 drop_pair。Detail 中的下标统一升序，保证确定性。
func recordProjectionDrop(actions *[]ProjectionAction, members []int, messages []Entry) {
	if len(members) == 1 {
		idx := members[0]
		*actions = append(*actions, ProjectionAction{
			Kind:   ProjectionDropMessage,
			Index:  idx,
			Detail: fmt.Sprintf("丢弃消息 #%d（角色 %s）", idx, messages[idx].Role),
		})
		return
	}
	var b strings.Builder
	b.WriteString("丢弃工具配对消息 [")
	for i, idx := range members {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "#%d", idx)
	}
	b.WriteString("]")
	*actions = append(*actions, ProjectionAction{
		Kind:   ProjectionDropPair,
		Index:  members[0],
		Detail: b.String(),
	})
}

// projectionProtected 计算需要保护（绝不丢弃）的消息下标集合。
func projectionProtected(messages []Entry, plan projectionPlan, policy ProjectionPolicy) []bool {
	n := len(messages)
	protected := make([]bool, n)
	mark := func(index int) {
		for _, idx := range plan.units[plan.unitOf[index]] {
			protected[idx] = true
		}
	}

	if policy.KeepRecentUser {
		for i := n - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				mark(i)
				break
			}
		}
	}

	if policy.KeepLiveToolTail {
		results := projectionResultIDs(messages)
		for i := n - 1; i >= 0; i-- {
			entry := messages[i]
			if entry.Role != "assistant" {
				break // 活尾只能是一段连续的 assistant 工具调用消息
			}
			unpaired := false
			for _, id := range projectionCallIDs(entry) {
				if len(results[id]) == 0 {
					unpaired = true
					break
				}
			}
			if !unpaired {
				break
			}
			mark(i)
		}
	}
	return protected
}

// projectionResultIDs 汇总每个工具调用 id 对应的 tool 结果下标。
func projectionResultIDs(messages []Entry) map[string][]int {
	out := make(map[string][]int)
	for i, entry := range messages {
		if entry.Role == "tool" && entry.ToolCallID != "" {
			out[entry.ToolCallID] = append(out[entry.ToolCallID], i)
		}
	}
	return out
}

// projectionCallIDs 提取一条 assistant 消息声明的工具调用 id。
// 优先解析持久化载荷中的 tool_calls / toolCalls；没有载荷时回退到 ToolCallID 字段。
func projectionCallIDs(entry Entry) []string {
	if entry.Role != "assistant" {
		return nil
	}
	if len(entry.Data) > 0 {
		var payload struct {
			Snake []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
			Camel []struct {
				ID string `json:"id"`
			} `json:"toolCalls"`
		}
		if err := json.Unmarshal(entry.Data, &payload); err == nil {
			ids := make([]string, 0, len(payload.Snake)+len(payload.Camel))
			for _, call := range payload.Snake {
				if call.ID != "" {
					ids = append(ids, call.ID)
				}
			}
			for _, call := range payload.Camel {
				if call.ID != "" {
					ids = append(ids, call.ID)
				}
			}
			if len(ids) > 0 {
				return ids
			}
		}
	}
	if entry.ToolCallID != "" {
		return []string{entry.ToolCallID}
	}
	return nil
}

// buildProjectionPlan 用并查集把由同一调用 id 关联的 assistant / tool 消息合并成单元。
func buildProjectionPlan(messages []Entry) projectionPlan {
	n := len(messages)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	callOwners := make(map[string][]int)
	results := projectionResultIDs(messages)
	for i, entry := range messages {
		for _, id := range projectionCallIDs(entry) {
			callOwners[id] = append(callOwners[id], i)
		}
	}
	for id, owners := range callOwners {
		// 同一 id 的重试调用必须同留同弃。
		for j := 1; j < len(owners); j++ {
			union(owners[0], owners[j])
		}
		// 调用与响应它的全部工具结果归入同一单元。
		// map 遍历顺序不影响并查集的最终连通分量（单元随后按下标排序）。
		for _, result := range results[id] {
			union(owners[0], result)
		}
	}

	groups := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], i) // i 升序追加，组内天然有序
	}

	plan := projectionPlan{
		unitOf: make([]int, n),
		units:  make([][]int, 0, len(groups)),
	}
	for _, members := range groups {
		uid := len(plan.units)
		plan.units = append(plan.units, members)
		for _, idx := range members {
			plan.unitOf[idx] = uid
		}
	}
	plan.order = make([]int, len(plan.units))
	for i := range plan.order {
		plan.order[i] = i
	}
	sort.Slice(plan.order, func(a, b int) bool {
		ua, ub := plan.units[plan.order[a]], plan.units[plan.order[b]]
		return ua[0] < ub[0]
	})
	return plan
}
