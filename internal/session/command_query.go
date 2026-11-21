package session

import "encoding/json"

// command_query.go 提供从会话记录中查询 shell 命令记录的纯函数。
//
// 这些函数只读入参，不修改 records 及其元素，也不修改 Data 的内容；
// 返回切片时一律分配新的底层数组（Record 为结构体副本，Data 仍为只读共享的载荷）。
// 记录按传入顺序（即文件顺序）稳定处理。

// hasToolCalls 报告载荷中是否带有非空的 tool_calls 数组。assistant 记录会持久化完整的
// llm.Message 载荷，规范键为 "tool_calls"，某些生产者使用 "toolCalls"，两者都识别；
// 空数组或 null 不构成命令形状。
func hasToolCalls(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	for _, key := range []string{"tool_calls", "toolCalls"} {
		field, ok := payload[key]
		if !ok {
			continue
		}
		var calls []json.RawMessage
		if json.Unmarshal(field, &calls) == nil && len(calls) > 0 {
			return true
		}
	}
	return false
}

// isCommandRecord 判断一条记录是否为 shell 命令记录。命中以下任一形状即为命令记录：
//   - messageEntry 投影后 Role=assistant 且载荷含 tool_calls：对应命令的发起；
//   - Kind=tool 且投影后的 Name 为 bash/powershell：对应命令的结果。
//
// messageEntry 返回的 Name 来自载荷的 "name"（或回退键 "tool"），因此两种写法都能识别。
func isCommandRecord(record Record) bool {
	entry, ok := messageEntry(record)
	if !ok {
		return false
	}
	if entry.Role == "assistant" && hasToolCalls(record.Data) {
		return true
	}
	return record.Kind == KindTool && (entry.Name == "bash" || entry.Name == "powershell")
}

// CommandRecords 返回所有 shell 命令记录，按传入顺序稳定排列。
// 返回的切片与入参不共享底层数组；入参不被修改。
func CommandRecords(records []Record) []Record {
	out := make([]Record, 0, len(records))
	for _, record := range records {
		if isCommandRecord(record) {
			out = append(out, record)
		}
	}
	return out
}

// LastCommand 返回最后一条（按文件顺序）shell 命令记录及存在标记。
// 没有命令记录时返回零值 Record 与 false；不修改入参。
func LastCommand(records []Record) (Record, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		if isCommandRecord(records[i]) {
			return records[i], true
		}
	}
	return Record{}, false
}

// CommandCountByKind 按记录的 Kind 统计各类 shell 命令记录的数量。
// 只统计实际出现的类别，不写入数量为零的键；重复调用结果一致；不修改入参。
func CommandCountByKind(records []Record) map[string]int {
	out := make(map[string]int)
	for _, record := range records {
		if isCommandRecord(record) {
			out[record.Kind]++
		}
	}
	return out
}
