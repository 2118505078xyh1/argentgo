package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// retentionKey 是 SetRetention 写入记录 JSON 数据包络的保留字段。
// 将保留策略放在现有的 Data 块中，意味着无需改动 Record 的 schema，也无需增加 JSONL 列；
// 而且 messageEntry 已逐字复制 Data 到 Entry.Data，因此该提示能到达对话视图。
const retentionKey = "_retention"

// Retention 是逐条记录的提示，指明记录必须保留多久。它管理两个独立的生命周期：
// 父线程的压缩，以及传播到子会话（子代理）。
type Retention uint8

const (
	// RetainDefault 是默认行为：记录可能被压缩掉，且不会复制到子会话。
	// 从未携带该提示的记录读回时也是 RetainDefault。
	RetainDefault Retention = iota
	// RetainAlways 既在压缩后保留该记录，又将其传播到子会话。
	RetainAlways
	// RetainInThread 仅在压缩后保留该记录；不传播到子会话。
	RetainInThread
	// RetainInChildren 只将记录传播到子会话；在父线程中它仍可能被压缩掉。
	RetainInChildren
)

// String 实现 fmt.Stringer。它返回 SetRetention 使用的稳定小写线格式值，
// 对于超出范围的值则返回描述性形式。
func (r Retention) String() string {
	switch r {
	case RetainDefault:
		return "default"
	case RetainAlways:
		return "always"
	case RetainInThread:
		return "in_thread"
	case RetainInChildren:
		return "in_children"
	default:
		return fmt.Sprintf("Retention(%d)", uint8(r))
	}
}

// SurvivesCompaction 报告 r 是否必须在压缩后保留。
func (r Retention) SurvivesCompaction() bool {
	return r == RetainAlways || r == RetainInThread
}

// SurvivesChildren 报告 r 是否必须植入子会话。
func (r Retention) SurvivesChildren() bool {
	return r == RetainAlways || r == RetainInChildren
}

// hasRetentionHint 报告 data 是否为带有字符串值 _retention 字段的 JSON 对象。
// 这里只看字段是否存在：即使是无法识别的值，也是调用方的显式选择，
// 而 GetRetention 已经把它映射为 RetainDefault。基于 kind 的自动默认值绝不能取代显式提示，
// 否则调用方对摘要请求（例如）in_thread 时，会静默得到 always，并把该记录泄漏到子会话。
func hasRetentionHint(data []byte) bool {
	var fields map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &fields) != nil || fields == nil {
		return false
	}
	raw, ok := fields[retentionKey]
	if !ok {
		return false
	}
	// json.Unmarshal(null, &string) 会以空字符串成功返回；null 提示表示"无提示"，
	// 因此仍适用自动默认值。
	if string(bytes.TrimSpace(raw)) == "null" {
		return false
	}
	var text string
	return json.Unmarshal(raw, &text) == nil
}

// parseRetention 读取存储的线格式值。它刻意宽松（去空白、忽略大小写），
// 使手写或较旧的记录仍能读回，并对任何无法识别的值回退到 RetainDefault。
func parseRetention(text string) Retention {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "always":
		return RetainAlways
	case "in_thread":
		return RetainInThread
	case "in_children":
		return RetainInChildren
	default:
		return RetainDefault
	}
}

// SetRetention 将 r 注入记录 JSON 数据包络中 retentionKey 对应的位置，
// 保留所有其他字段。空的 data 切片视为空对象。非空但不是 JSON 对象（或格式错误）的切片会报错：
// 保留一个无法保全其他字段的载荷，比拒绝标记它更糟糕。
func SetRetention(data []byte, r Retention) ([]byte, error) {
	fields := make(map[string]json.RawMessage)
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, fmt.Errorf("session: set retention: parse data: %w", err)
		}
	}
	if fields == nil { // JSON "null" 会反序列化为 nil map。
		fields = make(map[string]json.RawMessage)
	}

	value, err := json.Marshal(r.String())
	if err != nil {
		return nil, fmt.Errorf("session: set retention: encode value: %w", err)
	}
	fields[retentionKey] = value

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("session: set retention: encode data: %w", err)
	}
	return out, nil
}

// GetRetention 从记录的 JSON 数据包络中读取保留提示。
// 缺失、格式错误、非对象或无法识别的值都表示 RetainDefault，
// 因此在该提示出现之前写入的记录表现正常。
func GetRetention(data []byte) Retention {
	var fields map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &fields) != nil || fields == nil {
		return RetainDefault
	}
	raw, ok := fields[retentionKey]
	if !ok {
		return RetainDefault
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return RetainDefault
	}
	return parseRetention(text)
}

// SelectForCompaction 返回必须在压缩后保留的记录：标记为 RetainAlways 或 RetainInThread 的记录，
// 无论压缩器的切分点落在哪里。它是纯辅助函数；调用方决定如何把结果合并到保留的后缀中。
//
// budget 限制返回的记录数量（是条数，不是 token 估算）。非正数表示不设上限。
// 当上限迫使做出选择时，最近出现的带标记记录优先，与 KeepForBudget 的"最新上下文得以保留"规则一致。
// 结果按原始记录顺序排列。
func SelectForCompaction(records []Record, budget int) []Record {
	flagged := make([]Record, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		if !GetRetention(records[i].Data).SurvivesCompaction() {
			continue
		}
		flagged = append(flagged, records[i])
		if budget > 0 && len(flagged) == budget {
			break
		}
	}
	if len(flagged) == 0 {
		return nil
	}
	for i, j := 0, len(flagged)-1; i < j; i, j = i+1, j-1 {
		flagged[i], flagged[j] = flagged[j], flagged[i]
	}
	return flagged
}

// SelectForChildren 返回必须植入子会话的记录：标记为 RetainAlways 或 RetainInChildren 的记录，
// 按原始记录顺序排列。RetainInThread 被刻意排除——它在父线程中能熬过压缩，
// 但绝不能泄漏到子代理的上下文中。
func SelectForChildren(records []Record) []Record {
	var out []Record
	for _, record := range records {
		if GetRetention(record.Data).SurvivesChildren() {
			out = append(out, record)
		}
	}
	return out
}
