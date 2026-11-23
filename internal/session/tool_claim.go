package session

import (
	"encoding/json"
	"log"
	"strings"
)

// KindToolClaim 是非幂等工具调用的持久化领取记录。它只存在于 JSONL 转录中，
// 不会被 messageEntry 投影成模型可见消息，因此旧会话与干净路径逐字节不变。
const KindToolClaim = "tool_claim"

// ToolClaim 是领取记录的载荷。tool_call_id 是稳定的调用标识；
// result 在执行完成时可选写回，供重放时把原结果回灌给模型，避免重复副作用。
type ToolClaim struct {
	ToolCallID string `json:"tool_call_id"`
	Result     string `json:"result,omitempty"`
}

// ToolClaimState 是一次扫描得到的领取账本视图。Claimed 表示已领取的调用 id，
// Results 保存已有的执行结果；Malformed 统计损坏记录数（fail-open：记 warn 不阻断）。
type ToolClaimState struct {
	Claimed   map[string]bool
	Results   map[string]string
	Malformed int
}

// AppendToolClaim 在执行非幂等工具之前把领取记录落盘。
// 它是默认开启的安全账本：调用方必须在真正执行前调用它；返回错误时调用方
// 可以选择 fail-open 继续执行（TryAppendToolClaim 即为此语义）。
func (s *Session) AppendToolClaim(toolCallID string) error {
	id := strings.TrimSpace(toolCallID)
	if id == "" {
		return nil // 空标识不构成领取，避免产生无意义记录。
	}
	_, err := s.AppendRecord(KindToolClaim, ToolClaim{ToolCallID: id})
	return err
}

// TryAppendToolClaim 在领取落盘失败时记一条 warn 并返回 false。
// 账本不可写绝不阻断工具执行：接受重放风险优于让整轮运行卡死（fail-open）。
func (s *Session) TryAppendToolClaim(toolCallID string) bool {
	if err := s.AppendToolClaim(toolCallID); err != nil {
		log.Printf("session: warn: tool claim append failed (%v); proceeding without durable claim", err)
		return false
	}
	return true
}

// ScanToolClaims 从记录中收集领取账本。损坏或不可解析的 claim 被忽略并计数，
// 绝不返回错误；这使账本损坏时运行仍可继续，调用方可用 Malformed 记 warn。
func ScanToolClaims(records []Record) ToolClaimState {
	state := ToolClaimState{
		Claimed: make(map[string]bool),
		Results: make(map[string]string),
	}
	for _, record := range records {
		switch record.Kind {
		case KindToolClaim:
			var claim ToolClaim
			if err := json.Unmarshal(record.Data, &claim); err != nil || strings.TrimSpace(claim.ToolCallID) == "" {
				state.Malformed++
				continue
			}
			state.Claimed[claim.ToolCallID] = true
			if claim.Result != "" {
				state.Results[claim.ToolCallID] = claim.Result
			}
		case KindTool, "tool_result", "tool-result", "toolresult":
			// 原有工具结果记录作为回灌源：领取后重放时可以直接复用它，
			// 不必重新执行工具。
			entry, ok := messageEntry(record)
			if !ok || entry.ToolCallID == "" {
				continue
			}
			if _, exists := state.Results[entry.ToolCallID]; !exists {
				state.Results[entry.ToolCallID] = entry.Content
			}
		}
	}
	return state
}

// ToolClaims 返回当前活动分支上的领取账本视图。
// 分支相对：fork/checkout 后只看到本分支领取过的调用，不污染父分支。
func (s *Session) ToolClaims() (ToolClaimState, error) {
	records, err := s.Records()
	if err != nil {
		return ToolClaimState{}, err
	}
	return ScanToolClaims(s.activePath(records)), nil
}

// ShouldSkipToolCall 报告一次工具调用是否已被领取：已领取的重放必须跳过执行。
func ShouldSkipToolCall(state ToolClaimState, toolCallID string) bool {
	return state.Claimed[toolCallID]
}

// ResultForClaim 返回某次已领取调用的原结果，供跳过执行后回灌给模型。
func ResultForClaim(state ToolClaimState, toolCallID string) (string, bool) {
	result, ok := state.Results[toolCallID]
	return result, ok
}
