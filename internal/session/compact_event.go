package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// CondensationEvent 把"压缩"记录为一等事件，使遗忘行为可解释、可复现。
//
// ForgottenIDs 是本轮压缩永久遗忘的事件 ID 集合（已去重并按字典序排序）；
// Summary 是压缩生成的摘要文本；Offset 是摘要在会话中的偏移量。
// 字段名对应稳定的有线格式 forgotten_event_ids / summary / summary_offset。
type CondensationEvent struct {
	ForgottenIDs []string `json:"forgotten_event_ids"`
	Summary      string   `json:"summary"`
	Offset       int      `json:"summary_offset"`
}

// NewCondensationEvent 构造压缩事件。forgottenIDs 会被复制后去重、按字典序排序，
// 因此不会修改调用方传入的切片，且相同输入总能得到逐字节一致的稳定结果。
func NewCondensationEvent(forgottenIDs []string, summary string, offset int) CondensationEvent {
	return CondensationEvent{
		ForgottenIDs: normalizeForgottenIDs(forgottenIDs),
		Summary:      summary,
		Offset:       offset,
	}
}

// normalizeForgottenIDs 复制入参后去重并按字典序排序，返回全新切片。
// 复制保证调用方传入的切片不被改动；对字符串排序结果确定，因此输出稳定可复现。
// 空输入统一返回非 nil 的空切片，使 JSON 输出 [] 而不是 null。
func normalizeForgottenIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{}
	}
	out := append([]string(nil), ids...)
	sort.Strings(out)
	uniq := out[:0]
	for _, id := range out {
		if len(uniq) == 0 || uniq[len(uniq)-1] != id {
			uniq = append(uniq, id)
		}
	}
	return uniq
}

// Marshal 把压缩事件序列化为稳定的 JSON 字节。
//
// 稳定性体现在三点：字段固定（由 CondensationEvent 的声明顺序决定）、
// ForgottenIDs 总是数组（即便为空也输出 []）、中文不被转义（SetEscapeHTML(false)
// 让 CJK 原样输出而不是变成 \uXXXX）。即便事件被直接构造（未经 NewCondensationEvent），
// 这里也会再次归一化 ForgottenIDs，保证线上格式始终去重且有序。返回值不含末尾换行。
func (e CondensationEvent) Marshal() []byte {
	e.ForgottenIDs = normalizeForgottenIDs(e.ForgottenIDs)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 保留 CJK 原样，避免 \uXXXX 转义。
	if err := enc.Encode(e); err != nil {
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// ParseCondensationEvent 从 JSON 字节还原压缩事件，是 Marshal 的逆操作。
//
// 空输入、纯空白与 null 视为非法输入并返回错误；JSON 语法错误通过 %w 包装后返回，
// 调用方可用 errors.Is 判断。解析成功后 ForgottenIDs 同样被去重排序，
// 保证与 NewCondensationEvent 构造出的事件等价，便于逐字节复现与比较。
func ParseCondensationEvent(data []byte) (CondensationEvent, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return CondensationEvent{}, fmt.Errorf("session: empty condensation event JSON")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return CondensationEvent{}, fmt.Errorf("session: null condensation event JSON")
	}
	var e CondensationEvent
	if err := json.Unmarshal(trimmed, &e); err != nil {
		return CondensationEvent{}, fmt.Errorf("session: parse condensation event: %w", err)
	}
	e.ForgottenIDs = normalizeForgottenIDs(e.ForgottenIDs)
	return e, nil
}
