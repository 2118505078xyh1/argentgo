package config

import (
	"bytes"
	"encoding/json"
	"sort"
)

// IntegrityReport 是完整性审计结果的可序列化投影。
//
// Total 为缺失项总数，Fatal 为阻断启动的致命项数，Warnings 为非致命项数，
// 恒有 Total == Fatal + Warnings。Entries 是按 MissingEntryLess 稳定排序后的
// 副本，调用方可安全持有并序列化它，而不会影响传入 BuildIntegrityReport 的
// 原始切片。所有字段都不带 omitempty，便于脚本稳定消费。
type IntegrityReport struct {
	Total    int            `json:"total"`
	Fatal    int            `json:"fatal"`
	Warnings int            `json:"warnings"`
	Entries  []MissingEntry `json:"entries"`
}

// BuildIntegrityReport 把缺失项聚合成一份可序列化的完整性报告。
//
// 它是一个纯函数：只读取 entries，不修改调用方持有的元素顺序或内容。排序在
// 副本上进行，因此相同输入集合（即便初始顺序不同）会得到相同的 Entries 顺序；
// Entry 相等时由 sort.SliceStable 保留原始相对顺序。计数直接由入参得出，与排序
// 无关。
//
// 该函数默认没有调用点，仅作为 doctor 等诊断入口按需构造报告使用。
func BuildIntegrityReport(entries []MissingEntry) IntegrityReport {
	fatal := 0
	for _, e := range entries {
		if e.Fatal {
			fatal++
		}
	}

	// append 到 nil 切片生成副本：entries 为 nil 时副本也为 nil，排序 nil
	// 切片是安全的空操作；JSON() 会把 nil 规范化为 []。
	ordered := append([]MissingEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return MissingEntryLess(ordered[i], ordered[j])
	})

	return IntegrityReport{
		Total:    len(entries),
		Fatal:    fatal,
		Warnings: len(entries) - fatal,
		Entries:  ordered,
	}
}

// JSON 把报告序列化为稳定的 JSON 字节。
//
// 稳定性体现在：
//   - 字段顺序固定（由 IntegrityReport / MissingEntry 的声明顺序决定），
//     且不使用 omitempty，四个字段始终存在；
//   - 空 Entries（nil 或空切片）统一输出 []，避免 null 与 [] 因构造方式不同而漂移；
//   - SetEscapeHTML(false) 让 CJK 与 <、>、& 等字符按 UTF-8 原样输出，
//     不产生 \uXXXX 转义；
//   - 去掉 json.Encoder 追加的末尾换行，返回值不含尾随 \n。
//
// 该方法不修改接收者，相同报告总是返回逐字节一致的切片。
func (r IntegrityReport) JSON() []byte {
	// 复制结构体头部，仅把 nil 归一化为非 nil 空切片；不触碰接收者的切片。
	report := r
	if report.Entries == nil {
		report.Entries = []MissingEntry{}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// IntegrityReport 全由基本类型与可编码结构体组成，Encode 不会失败。
	_ = enc.Encode(report)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
