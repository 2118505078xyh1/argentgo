// Package session —— JSONL 转录的完整性校验原语。
//
// 会话以 JSONL 存储：一行一条记录，记录以 id/parent 相连成树。加载路径刻意宽松
// （跳过空白与格式错误的行），这在读者侧很稳健，但无法回答"这份转录是否完整"。
// ValidateJSONL 提供一个独立、纯函数的诊断视角，供 doctor / 诊断工具在不改动
// 加载路径的前提下检查转录的结构完整性。
package session

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// JSONL 问题种类。Kind 的取值稳定，doctor 可直接据此分类；Detail 为英文且
// 与语言环境无关（与 CronIssue.Message 一致），因此诊断输出不随 zh-CN/en-US 变化。
const (
	JSONLKindInvalidJSON    = "invalid_json"    // 行不是合法 JSON 对象，或 id/parent 不是字符串
	JSONLKindMissingID      = "missing_id"      // 记录缺少非空 id
	JSONLKindDuplicateID    = "duplicate_id"    // id 在文件中多次出现
	JSONLKindDanglingParent = "dangling_parent" // parent 不指向文件中任何记录的 id
	JSONLKindSelfParent     = "self_parent"     // 记录的 parent 等于自身 id
)

// JSONLIssue 是 ValidateJSONL 发现的一个问题。Line 是从 1 开始的行号
// （与编辑器/诊断工具的显示一致）；空行也计入行号但会被跳过，不会产生问题。
type JSONLIssue struct {
	Line   int
	Kind   string
	Detail string
}

// String 将问题渲染为带行号的单行可读文本，便于 doctor 直接打印。
func (i JSONLIssue) String() string {
	return fmt.Sprintf("line %d: %s: %s", i.Line, i.Kind, i.Detail)
}

// jsonlWire 只解码完整性关心的字段；ts/kind/data 等其余字段原样忽略，
// 因此本校验器不会因时间戳畸形而误报——它只回答 id/parent 树是否连得起来。
type jsonlWire struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
}

// jsonlParsedLine 保存一行的解码结果，供第二遍按行号顺序发出诊断。
type jsonlParsedLine struct {
	number int
	wire   jsonlWire
	err    error // 非 nil 表示该行不是合法 JSON 对象（或 id/parent 类型不对）
}

// ValidateJSONL 逐行校验一段 JSONL 转录，返回按行号排序、稳定的问题列表。
//
// 逐行规则：
//   - 空行（含仅含空白的行）跳过，不产生问题，但仍占据行号；
//   - 非空白行必须是合法 JSON 对象且 id/parent 为字符串，否则记 invalid_json；
//   - id 非空，否则记 missing_id；
//   - id 在整份文件中只能出现一次，重复出现记 duplicate_id（Detail 指向首次行号）；
//   - parent 非空时，若等于自身 id 记 self_parent；
//   - parent 非空且不等于自身 id 时，若不等于文件中任何（首次出现的）id，
//     记 dangling_parent。parent 可以指向文件中更靠后的记录，因此实现分两遍：
//     先收集全部 id，再按行发诊断。
//
// id 作为不透明 UTF-8 字符串比较，CJK id 与 ASCII id 一视同仁，不会产生误报。
// 本函数是纯函数：不修改 data，结果只由 data 决定，对同一输入多次调用完全一致；
// 合法转录返回空但非 nil 的切片。
func ValidateJSONL(data []byte) []JSONLIssue {
	issues := make([]JSONLIssue, 0)

	// 第一遍：解析每个非空白行，收集首次出现的 id。
	parsed := make([]jsonlParsedLine, 0)
	firstSeen := make(map[string]int)
	for i, raw := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		number := i + 1
		var wire jsonlWire
		err := json.Unmarshal(line, &wire)
		parsed = append(parsed, jsonlParsedLine{number: number, wire: wire, err: err})
		if err == nil && wire.ID != "" {
			if _, exists := firstSeen[wire.ID]; !exists {
				firstSeen[wire.ID] = number
			}
		}
	}

	// 第二遍：按行号顺序发出诊断；遍历切片而非 map，保证顺序稳定、确定。
	for _, line := range parsed {
		if line.err != nil {
			issues = append(issues, JSONLIssue{
				Line:   line.number,
				Kind:   JSONLKindInvalidJSON,
				Detail: fmt.Sprintf("invalid JSON: %v", line.err),
			})
			continue
		}
		id, parent := line.wire.ID, line.wire.Parent
		if id == "" {
			issues = append(issues, JSONLIssue{
				Line:   line.number,
				Kind:   JSONLKindMissingID,
				Detail: "record has no id",
			})
		} else if first, ok := firstSeen[id]; ok && first != line.number {
			issues = append(issues, JSONLIssue{
				Line:   line.number,
				Kind:   JSONLKindDuplicateID,
				Detail: fmt.Sprintf("duplicate id %q (first seen on line %d)", id, first),
			})
		}
		if parent == "" {
			continue
		}
		if id != "" && parent == id {
			issues = append(issues, JSONLIssue{
				Line:   line.number,
				Kind:   JSONLKindSelfParent,
				Detail: fmt.Sprintf("record %q references itself as parent", id),
			})
			continue
		}
		if _, ok := firstSeen[parent]; !ok {
			detail := fmt.Sprintf("parent %q is not a known id", parent)
			if id != "" {
				detail = fmt.Sprintf("record %q has parent %q which is not a known id", id, parent)
			}
			issues = append(issues, JSONLIssue{
				Line:   line.number,
				Kind:   JSONLKindDanglingParent,
				Detail: detail,
			})
		}
	}
	return issues
}
