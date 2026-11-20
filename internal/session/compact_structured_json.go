package session

import (
	"bytes"
	"encoding/json"
	"time"
)

// structuredSummaryJSONDTO 是 structuredSummaryJSON 使用的稳定有线格式。
//
// 字段按固定顺序声明：encoding/json 依据结构体声明顺序编码，因此无论会话内容如何，
// 字段名及其顺序始终一致；同时刻意不使用 omitempty，让三个桶始终以数组出现，
// 便于 CLI/API 稳定消费并逐字节比较。
type structuredSummaryJSONDTO struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Updated       time.Time `json:"updated"`
	Turns         int       `json:"turns"`
	Progress      []string  `json:"progress"`
	Uncertainties []string  `json:"uncertainties"`
	UserActions   []string  `json:"userActions"`
}

// structuredSummaryJSON 把结构化摘要连同会话元数据序列化为稳定的 JSON 字节。
//
// 稳定性体现在两点：字段固定（由 structuredSummaryJSONDTO 的声明顺序决定，且三桶
// 不做 omitempty），以及中文不被转义（SetEscapeHTML(false) 让 CJK 与 <、>、& 等字符
// 原样输出，而不是变成 \uXXXX）。返回值不含末尾换行。
//
// 摘要为空（三桶都没有条目）时返回 nil，调用方应继续使用原始自由文本。
// meta 为 nil 时以零值填充元数据字段，不会 panic。
func structuredSummaryJSON(meta *Meta, summary StructuredSummary) []byte {
	if summaryIsEmpty(summary) {
		return nil
	}
	var m Meta
	if meta != nil {
		m = *meta
	}
	dto := structuredSummaryJSONDTO{
		ID:            m.ID,
		Title:         m.Title,
		Updated:       m.Updated,
		Turns:         m.Turns,
		Progress:      nonNilStrings(summary.Progress),
		Uncertainties: nonNilStrings(summary.Uncertainties),
		UserActions:   nonNilStrings(summary.UserActions),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 保留 CJK 与 HTML 字符原样，避免 \uXXXX 转义。
	if err := enc.Encode(dto); err != nil {
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// nonNilStrings 保证切片非 nil，使 JSON 输出为 [] 而不是 null。
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// StructuredJSON 是本压缩记录的结构化摘要投影与会话元数据的稳定 JSON 表示。
// 它是 Compaction.Structured() 的只读接线：当摘要为空或格式漂移（Structured 返回
// false）时返回 nil，调用方应回退到 Compaction.Summary 的原始自由文本。
// meta 为 nil 时以零值填充元数据字段。
func (c Compaction) StructuredJSON(meta *Meta) []byte {
	summary, ok := c.Structured()
	if !ok {
		return nil
	}
	return structuredSummaryJSON(meta, summary)
}
