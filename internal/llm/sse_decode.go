package llm

import (
	"encoding/json"
	"strings"
)

// splitSSEData 把一个 SSE 事件的数据载荷规范化为它所携带的 JSON 事件。
// 共享的 sseReader 按 SSE 规范用换行符连接多行 data: 字段。
// 两种网关形态需要额外处理：
//
//   - 单个 JSON 对象被拆到多个 data: 行（代理包装
//     长行，或拆分落在 JSON 字符串内部）：移除合成换行符即可恢复合法 JSON；
//     移除合成换行符即可恢复合法 JSON；
//   - 多个独立事件作为相邻 data: 行发出且没有空行
//     分隔：每行都是独立事件，必须按顺序解码。
//
// 常见的单事件情况返回未经改动的载荷（单元素切片）。
// 每个协议客户端都让载荷经过这个辅助函数，
// 从而容忍相同的线协议形态。
func splitSSEData(data string) []string {
	if !strings.Contains(data, "\n") {
		return []string{data}
	}
	if compact := strings.ReplaceAll(data, "\n", ""); strings.HasPrefix(compact, "{") && json.Valid([]byte(compact)) {
		return []string{compact}
	}
	if lines := strings.Split(data, "\n"); allJSONValues(lines) {
		return lines
	}
	return []string{data}
}

// isEmptySSEData 报告某 SSE 事件的数据载荷是否不含数据。
// 网关和负载均衡器会合法地发送空的 "data:" 行（或纯空白行）
// 作为原本空闲流上的保活信号。SSE 规范
// 将其视为没有载荷的事件，而非格式错误的 JSON 事件，因此
// 每个协议客户端都必须跳过它，而不是以
// "decode stream event" 错误中止回复。
func isEmptySSEData(data string) bool {
	return strings.TrimSpace(data) == ""
}

// allJSONValues 报告每行是否都是独立的 JSON 值（或 [DONE] 标记），
// 这意味着网关在本应独立的事件之间
// 省略了空行。
func allJSONValues(lines []string) bool {
	if len(lines) < 2 {
		return false
	}
	for _, line := range lines {
		if line == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(line)) {
			return false
		}
	}
	return true
}
