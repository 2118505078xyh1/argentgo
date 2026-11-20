package session

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 本文件实现压缩摘要中确定性的、无需 LLM 的"工具活动"部分。当一段对话被丢弃后，
// 下一轮仍需要知道读取了哪些文件（以及哪些行）、修改了哪些文件、运行了哪些命令——
// 否则 agent 会重复读取相同的文件，或忘记它已经做过的改动。与目标/失败/下一步的提取不同，
// 这里没有调用模型：它直接从被丢弃的条目中读取记录下来的工具调用。

const (
	activityCommandLimit = 100 // 对命令行进行类似 cline 的截断
	activityPathLimit    = 120 // 防止路径占满整个项目符号
	activityBulletLimit  = 200 // 单条渲染活动行的硬上限
	activityMaxBullets   = 24  // 无论转录多大都保持摘要有界
)

// activityReadTools 是其参数描述读取文件的工具。记录下来的参数携带 path/file/file_path
// 之一，再加上 offset/limit 或以行号为中心的窗口（line/before/after），
// 从中重建出 path:start-end 范围。
var activityReadTools = map[string]bool{"read": true, "cat": true}

// activityEditTools 是修改文件的工具。从记录下来的编辑中只能恢复路径：
// 工具结果只是一行裸的 "edited <path>"，没有 diff，因此不触碰文件就无法得出可靠的行范围
// （纯函数不应触碰文件）。
var activityEditTools = map[string]bool{
	"edit": true, "write": true, "multi_edit": true, "patch": true,
}

// activityCommandTools 是参数中携带 shell 命令的工具。
var activityCommandTools = map[string]bool{
	"bash": true, "shell": true, "sh": true, "powershell": true, "pwsh": true,
}

// activityCall 是从转录记录中投影出的一次工具调用：一个名称及其解码后的参数。
// 它只通过 map 查找构建，绝不通过 map 迭代，因此提取是确定性的。
type activityCall struct {
	name string
	args map[string]any
}

// summarizeToolActivity 根据 dropped 中的读取/编辑/命令调用返回一个 markdown
// "Tool activity" 章节；没有此类活动时返回 ""。它是纯函数：确定、去重且大小有界。
func summarizeToolActivity(dropped []Entry) string {
	var bullets []string
	seen := make(map[string]bool)
	for _, entry := range dropped {
		for _, call := range callsFromData(entry.Data) {
			bullet, ok := activityBullet(call)
			if !ok || bullet == "" || seen[bullet] {
				continue
			}
			seen[bullet] = true
			bullets = append(bullets, bullet)
		}
	}
	if len(bullets) == 0 {
		return ""
	}
	// 保持时间顺序；当活动过多时，保留最近的活动
	// （恢复的轮次最可能需要这些）。
	bullets = tailItems(bullets, activityMaxBullets)

	var b strings.Builder
	b.WriteString("\n## Tool activity\n")
	for _, bullet := range bullets {
		fmt.Fprintf(&b, "- %s\n", truncateRunes(bullet, activityBulletLimit))
	}
	return b.String()
}

// callsFromData 从一条记录的载荷中提取工具调用。会话文件中会出现两种布局：
//   - 工具结果记录，顶层带有 "name" 和 "arguments"；
//   - assistant 记录，带有 "tool_calls"（或 "toolCalls"）数组，
//     其元素携带 function.name 和 function.arguments。
func callsFromData(raw json.RawMessage) []activityCall {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	var out []activityCall
	for _, key := range []string{"tool_calls", "toolCalls"} {
		list, ok := object[key].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			call, ok := item.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := call["function"].(map[string]any)
			name := ""
			if fn != nil {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = call["name"].(string)
			}
			if name == "" {
				continue
			}
			args := decodedArgs(fn["arguments"])
			if args == nil {
				args = decodedArgs(call["arguments"])
			}
			out = append(out, activityCall{name: name, args: args})
		}
	}

	name, _ := object["name"].(string)
	if name != "" && name != "tool" {
		args := decodedArgs(object["arguments"])
		if args == nil {
			args = decodedArgs(object["args"])
		}
		if args == nil {
			args = decodedArgs(object["input"])
		}
		out = append(out, activityCall{name: name, args: args})
	}
	return out
}

// decodedArgs 接受以 JSON 字符串序列化的参数（工具记录中的常见情况），
// 或已解码的 JSON 对象。
func decodedArgs(raw any) map[string]any {
	switch typed := raw.(type) {
	case string:
		var object map[string]any
		if json.Unmarshal([]byte(typed), &object) == nil {
			return object
		}
	case map[string]any:
		return typed
	}
	return nil
}

// activityBullet 将一次工具调用渲染为单行活动记录。未知工具以及缺少预期参数的调用会被忽略。
func activityBullet(call activityCall) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(call.name))
	args := call.args
	switch {
	case activityReadTools[name]:
		path := activityPath(args)
		if path == "" {
			return "", false
		}
		return "read " + activityPathRange(path, args), true
	case activityEditTools[name]:
		path := activityPath(args)
		if path == "" {
			return "", false
		}
		return "edit " + truncateRunes(path, activityPathLimit), true
	case activityCommandTools[name]:
		command := ""
		for _, key := range []string{"command", "cmd"} {
			if text, ok := args[key].(string); ok {
				command = text
				break
			}
		}
		if strings.TrimSpace(command) == "" {
			return "", false
		}
		return "bash " + truncateRunes(command, activityCommandLimit), true
	}
	return "", false
}

// activityPath 返回工具调用所指的文件，优先使用内置工具实际使用的键。
func activityPath(args map[string]any) string {
	for _, key := range []string{"path", "file", "file_path"} {
		if text, ok := args[key].(string); ok {
			if text = strings.TrimSpace(text); text != "" {
				return text
			}
		}
	}
	return ""
}

// activityPathRange 在读取路径后附加行范围。显式的窗口参数优先；
// 否则把路径中已嵌入的行选择器（":10"、":10:20"、"#L10"、"#L10-L20"）
// 规范化为 "path:start-end"。
func activityPathRange(path string, args map[string]any) string {
	line := activityInt(args, "line")
	if line > 0 {
		start := line - activityInt(args, "before")
		if start < 1 {
			start = 1
		}
		end := line + activityInt(args, "after")
		if end < start {
			end = start
		}
		return activityFormatPath(path, start, end)
	}
	offset := activityInt(args, "offset")
	limit := activityInt(args, "limit")
	if offset > 0 || limit > 0 {
		start := offset
		if start < 1 {
			start = 1
		}
		end := start
		if limit > 0 {
			end = start + limit - 1
		}
		if end < start {
			end = start
		}
		return activityFormatPath(path, start, end)
	}
	if base, start, end, ok := splitTrailingRange(path); ok {
		return activityFormatPath(base, start, end)
	}
	return truncateRunes(path, activityPathLimit)
}

func activityFormatPath(path string, start, end int) string {
	return fmt.Sprintf("%s:%d-%d", truncateRunes(path, activityPathLimit), start, end)
}

// activityInt 读取整数参数。encoding/json 在反序列化到 any 时把数字解码为 float64，
// 这里正是这种常见情况。
func activityInt(args map[string]any, key string) int {
	switch typed := args[key].(type) {
	case float64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	case int:
		return typed
	}
	return 0
}

// splitTrailingRange 识别路径中嵌入的行选择器，返回去掉选择器的路径以及含端点的起始/结束行。
// 它小心避免把 "C:\dir" 误认为选择器（冒号后面必须跟数字），
// 没有选择器时返回 ok=false。
func splitTrailingRange(path string) (base string, start, end int, ok bool) {
	if idx := strings.LastIndex(path, "#"); idx >= 0 {
		base = path[:idx]
		start, end, ok = activityRangeToken(path[idx+1:])
		if !ok {
			return "", 0, 0, false
		}
		return base, start, end, true
	}

	i := len(path)
	var nums []int
	for i > 0 {
		j := i - 1
		if path[j] < '0' || path[j] > '9' {
			break
		}
		k := j
		for k > 0 && path[k-1] >= '0' && path[k-1] <= '9' {
			k--
		}
		if k == 0 || path[k-1] != ':' {
			break
		}
		n, err := strconv.Atoi(path[k : j+1])
		if err != nil {
			break
		}
		nums = append([]int{n}, nums...)
		i = k - 1
	}
	if len(nums) == 0 {
		return "", 0, 0, false
	}
	start, end = nums[0], nums[0]
	if len(nums) > 1 {
		end = nums[len(nums)-1]
	}
	if end < start {
		start, end = end, start
	}
	return path[:i], start, end, true
}

// activityRangeToken 将 "#" 之后的部分（例如 "L10"、"10-20" 或 "L10-L20"）
// 解析为含端点的行范围。
func activityRangeToken(token string) (start, end int, ok bool) {
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == '-' || r == ':' })
	if len(parts) == 0 {
		return 0, 0, false
	}
	// 每个组件都可以自带 "L" 前缀，例如 "L10-L20"。
	partInt := func(part string) (int, bool) {
		part = strings.TrimPrefix(strings.TrimPrefix(part, "L"), "l")
		n, err := strconv.Atoi(part)
		return n, err == nil && n > 0
	}
	start, ok = partInt(parts[0])
	if !ok {
		return 0, 0, false
	}
	end = start
	if len(parts) > 1 {
		if n, ok := partInt(parts[1]); ok {
			end = n
		}
	}
	if end < start {
		start, end = end, start
	}
	return start, end, true
}
