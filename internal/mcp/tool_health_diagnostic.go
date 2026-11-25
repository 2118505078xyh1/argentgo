package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/llm"
)

// ToolHealth 是单个工具的健康诊断结果。字段均为值类型，由 DiagnoseTools 从
// 工具声明与注入的可用性检查逐字段计算而来，与入参完全隔离。
type ToolHealth struct {
	Name        string // 工具名，取自 ToolSpec.Function.Name
	Available   bool   // 注入的 check 是否报告该工具可用
	SchemaValid bool   // 参数 schema 是否为合法 JSON（json.Valid）
	Degraded    bool   // 可用但 schema 非法：能调用但存在缺陷，属于“降级”而非“不可用”
	Reason      string // 稳定的中英双语原因，格式见 diagnoseReason
}

// DiagnoseTools 对一组工具声明做纯函数式健康诊断。
//
// 行为约定：
//   - 输出与 specs 一一对应、顺序一致，不排序、不去重；
//   - 工具名取 spec.Function.Name，schema 取 spec.Function.Parameters；
//   - Available 由注入的 check(name) 决定；check 为 nil 时视为全部不可用，
//     以避免对 nil 函数直接调用而 panic；
//   - SchemaValid 严格按 json.Valid 判定：nil/空的参数字节也被视为非法；
//   - Degraded = Available && !SchemaValid，即“在线但有缺陷”，真正的不可用
//     不归为降级；
//   - 只读入参、返回全新的结果切片，不与 specs/check 产生任何别名；
//   - 全程不截断字符串，CJK 工具名与 schema 内容按原始字节保留，安全。
func DiagnoseTools(specs []llm.ToolSpec, check func(string) bool) []ToolHealth {
	out := make([]ToolHealth, len(specs))
	for i := range specs {
		name := specs[i].Function.Name
		available := check != nil && check(name)
		schemaValid := json.Valid(specs[i].Function.Parameters)
		out[i] = ToolHealth{
			Name:        name,
			Available:   available,
			SchemaValid: schemaValid,
			Degraded:    available && !schemaValid,
			Reason:      diagnoseReason(available, schemaValid),
		}
	}
	return out
}

// diagnoseReason 根据可用性与 schema 合法性拼装稳定的中英双语原因。
// 始终按固定顺序拼接，组合原因用 "; " 分隔，保证逐字节可复现。
func diagnoseReason(available, schemaValid bool) string {
	if available && schemaValid {
		return "正常/ok"
	}
	parts := make([]string, 0, 2)
	if !available {
		parts = append(parts, "不可用/unavailable")
	}
	if !schemaValid {
		parts = append(parts, "schema 非法/invalid schema")
	}
	return strings.Join(parts, "; ")
}

// ToolHealthSummary 把一组诊断结果汇总为一行稳定的中文摘要。
// 只统计计数、与输入顺序无关；空输入返回全 0 的摘要。
func ToolHealthSummary(hs []ToolHealth) string {
	available, unavailable, schemaOK, schemaBad, degraded := 0, 0, 0, 0, 0
	for _, h := range hs {
		if h.Available {
			available++
		} else {
			unavailable++
		}
		if h.SchemaValid {
			schemaOK++
		} else {
			schemaBad++
		}
		if h.Degraded {
			degraded++
		}
	}
	return fmt.Sprintf("工具诊断：共 %d 个，可用 %d，不可用 %d，schema 合法 %d，schema 非法 %d，降级 %d。",
		len(hs), available, unavailable, schemaOK, schemaBad, degraded)
}
