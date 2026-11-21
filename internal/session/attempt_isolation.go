package session

import (
	"encoding/json"
	"strings"
)

// 本文件提供"失败尝试隔离"的纯函数原语：在不改写会话历史的前提下，
// 识别属于某次尝试的失败记录、判断其是否可安全丢弃，并给出回退的分叉点。
// 所有函数只读入参、零 I/O，且不依赖 agent/llm/app 包。

// AttemptMarker 标记一条属于某次尝试且带有失败信号的记录。
type AttemptMarker struct {
	AttemptID string // 所属尝试的 id
	ParentID  string // 被标记记录在会话树中的父 id
	Outcome   string // 结果：AttemptFailed 或 attemptOutcomeSucceeded
	TS        int64  // 被标记记录的时间戳（Unix 秒）
}

const (
	// attemptOutcomeSucceeded 表示一次成功的结果，用于让 CanDiscardAttempt 排除可丢弃性。
	attemptOutcomeSucceeded = "succeeded"
)

// attemptIDDelimiters 是 id 前缀归属时认可的分隔符。要求前缀之后紧跟分隔符，
// 避免把 "att-10" 的记录错误地归入 "att-1" 尝试（隔离必须精确）。
const attemptIDDelimiters = "-_:/. #"

// stopErrorSignals 是 security.StopError 文本中的稳定特征。session 不 import security，
// 因此这里按文本识别，措辞与 security 包中的最终性语句保持一致。
var stopErrorSignals = []string{
	"this block is intentional and final",
	"tool call was denied",
	"tool call was refused",
}

// toolOutputKeys 是 KindTool 载荷中需要扫描失败文本的字符串字段。
// 只扫描输出类字段而非整个 JSON，避免把工具名（如 "error-checker"）误判为失败。
var toolOutputKeys = []string{"output", "content", "text", "result", "error", "message", "stderr"}

// idInAttempt 判断 id 是否归属 attemptID：精确相等，或以 attemptID 为前缀且其后紧跟分隔符。
// 前缀本身以分隔符结尾时不再要求额外边界。
func idInAttempt(id, attemptID string) bool {
	if attemptID == "" || id == "" {
		return false
	}
	if id == attemptID {
		return true
	}
	if !strings.HasPrefix(id, attemptID) {
		return false
	}
	rest := id[len(attemptID):]
	if rest == "" {
		return true
	}
	if strings.ContainsRune(attemptIDDelimiters, rune(attemptID[len(attemptID)-1])) {
		return true
	}
	return strings.ContainsRune(attemptIDDelimiters, rune(rest[0]))
}

// belongsToAttempt 判断一条记录是否属于某次尝试：
// 自身 id 归属，或沿 parent 链上溯能找到归属的祖先。
func belongsToAttempt(record Record, attemptID string, byID map[string]Record) bool {
	if idInAttempt(record.ID, attemptID) {
		return true
	}
	seen := map[string]bool{record.ID: true}
	parent := record.Parent
	for parent != "" && !seen[parent] {
		seen[parent] = true
		ancestor, ok := byID[parent]
		if !ok {
			break
		}
		if idInAttempt(ancestor.ID, attemptID) {
			return true
		}
		parent = ancestor.Parent
	}
	return false
}

// hasStopErrorSignal 按文本识别 StopError 的最终性措辞。
func hasStopErrorSignal(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	lower := strings.ToLower(string(raw))
	for _, signal := range stopErrorSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// rawIsTrue 兼容 is_error 的布尔与字符串两种写法。
func rawIsTrue(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.EqualFold(strings.TrimSpace(s), "true")
	}
	return false
}

// toolRecordFailed 判断一条 KindTool 载荷是否携带失败信号：
// is_error 为真，或输出类字段的文本命中失败提示（复用 compact.go 的 failureLine）。
func toolRecordFailed(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		// 载荷本身可能是 JSON 字符串。
		var text string
		return json.Unmarshal(raw, &text) == nil && failureLine(text, 200) != ""
	}
	if flag, ok := fields["is_error"]; ok && rawIsTrue(flag) {
		return true
	}
	for _, key := range toolOutputKeys {
		value, ok := fields[key]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) == nil && failureLine(text, 200) != "" {
			return true
		}
	}
	return false
}

// recordFailureOutcome 返回记录的失败结果；非失败记录返回 ("", false)。
func recordFailureOutcome(record Record) (string, bool) {
	if hasStopErrorSignal(record.Data) {
		return AttemptFailed, true
	}
	if record.Kind == KindTool && toolRecordFailed(record.Data) {
		return AttemptFailed, true
	}
	return "", false
}

// MarkFailure 扫描记录流，返回属于 attemptID 且带失败信号的标记，
// 按记录的输入顺序稳定排列。它是纯函数：不修改 records，不做任何 I/O。
func MarkFailure(records []Record, attemptID string) []AttemptMarker {
	if attemptID == "" || len(records) == 0 {
		return nil
	}
	byID := indexByID(records)
	out := make([]AttemptMarker, 0)
	for _, record := range records {
		if !belongsToAttempt(record, attemptID, byID) {
			continue
		}
		outcome, ok := recordFailureOutcome(record)
		if !ok {
			continue
		}
		out = append(out, AttemptMarker{
			AttemptID: attemptID,
			ParentID:  record.Parent,
			Outcome:   outcome,
			TS:        record.TS.Unix(),
		})
	}
	return out
}

// CanDiscardAttempt 报告 id 对应尝试的所有标记是否都是失败结果。
// 没有任何标记时返回 false：没有失败证据就不应丢弃历史。
func CanDiscardAttempt(markers []AttemptMarker, id string) bool {
	if id == "" {
		return false
	}
	found := false
	for _, marker := range markers {
		if marker.AttemptID != id {
			continue
		}
		found = true
		if marker.Outcome != AttemptFailed {
			return false
		}
	}
	return found
}

// BranchPointFor 返回 attemptID 这棵子树在会话树中的分叉点（子树根的父 id），
// 供回退重试时使用。找不到属于该尝试的记录、或子树根没有父节点时返回 ("", false)。
func BranchPointFor(records []Record, attemptID string) (string, bool) {
	if attemptID == "" || len(records) == 0 {
		return "", false
	}
	byID := indexByID(records)
	members := make([]Record, 0)
	for _, record := range records {
		if belongsToAttempt(record, attemptID, byID) {
			members = append(members, record)
		}
	}
	if len(members) == 0 {
		return "", false
	}
	memberIDs := make(map[string]bool, len(members))
	for _, record := range members {
		memberIDs[record.ID] = true
	}
	// 按输入顺序找到第一个父节点不在子树内的记录，它就是子树根。
	for _, record := range members {
		if record.Parent == "" {
			return "", false
		}
		if !memberIDs[record.Parent] {
			return record.Parent, true
		}
	}
	return "", false
}
