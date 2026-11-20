package mcp

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
)

// contentAudienceUser 是 MCP content block annotations.audience 中
// 代表“最终用户”的角色。只有受众包含该角色的内容块才允许进入可读输出。
const contentAudienceUser = "user"

const (
	// contentFilterMinPriorityEnv 是可选的基于优先级的过滤阈值。
	// 未设置、非数字或小于等于 0 时关闭过滤，保证 CallTool 输出
	// 与历史行为逐字节一致。
	contentFilterMinPriorityEnv = "ARGENT_MCP_CONTENT_FILTER_MIN_PRIORITY"
)

// ContentBlockAnnotations 是 MCP content block annotations 中与可见性
// 过滤相关的字段。MCP 规范把 priority 定义为 0-1 的浮点数；这里把它
// 放大 100 倍存为 0-100 的整数，数值越大越重要。
//
// 缺省（audience 为 nil、priority 为 nil）时按最宽松处理：对 user 可见，
// 且视为最高优先级。因此未声明 annotations 的内容块永远不会被过滤。
type ContentBlockAnnotations struct {
	// Audience 描述内容面向的角色，如 "user"、"assistant"。
	// 缺省或为空表示对所有角色可见。
	Audience []string `json:"audience,omitempty"`
	// Priority 是 0-100 的优先级；nil 表示未声明，按最高优先级处理。
	Priority *int `json:"priority,omitempty"`
}

// UnmarshalJSON 容错解析 annotations：非法输入一律按“未声明”处理，
// 绝不因为坏 annotations 让整次 tools/call 失败。各成员独立解析，
// 某个字段类型错误不会丢弃其他字段。
func (a *ContentBlockAnnotations) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil
	}
	if raw, ok := fields["audience"]; ok {
		var audience []string
		if json.Unmarshal(raw, &audience) == nil {
			a.Audience = audience
		}
	}
	if raw, ok := fields["priority"]; ok {
		// 用指针接收以区分 JSON null（未声明）与数值 0。
		var value *float64
		if json.Unmarshal(raw, &value) == nil && value != nil {
			normalized := int(math.Round(*value * 100))
			if normalized < 0 {
				normalized = 0
			}
			if normalized > 100 {
				normalized = 100
			}
			a.Priority = &normalized
		}
	}
	return nil
}

// ContentBlock 是 tools/call 结果中的一个内容块。它在 CallTool 历史内联
// 解码结构的基础上增加了 annotations，用于可选的 audience/priority 过滤；
// 原有字段的名称、类型和解码行为完全不变。
type ContentBlock struct {
	Type        string                  `json:"type"`
	Text        string                  `json:"text"`
	MIMEType    string                  `json:"mimeType"`
	Resource    ResourceContent         `json:"resource"`
	Annotations ContentBlockAnnotations `json:"annotations,omitempty"`
}

// filterContentBlocks 按 MCP content block annotations 过滤内容块，返回新切片，
// 不修改入参及其元素：
//   - audience 非空且不含 "user" 的内容块被丢弃（内部内容不污染可读输出）；
//   - priority 已声明且低于 minPriority 的内容块被丢弃；
//   - audience 缺省视为对 user 可见，priority 缺省视为最高优先级。
//
// minPriority 传 0（或更小）时不做优先级过滤。本函数是纯函数：无 I/O、
// 无全局状态、无 CJK 文本切分，内容块的文本按原字节原样保留。
func filterContentBlocks(blocks []ContentBlock, minPriority int) []ContentBlock {
	if len(blocks) == 0 {
		return blocks
	}
	// 全部保留时直接返回原切片，确保缺省配置下零拷贝、零行为变化。
	kept := 0
	for _, block := range blocks {
		if contentBlockVisible(block, minPriority) {
			kept++
		}
	}
	if kept == len(blocks) {
		return blocks
	}
	out := make([]ContentBlock, 0, kept)
	for _, block := range blocks {
		if contentBlockVisible(block, minPriority) {
			out = append(out, block)
		}
	}
	return out
}

// contentBlockVisible 报告单个内容块是否对最终用户可见。
func contentBlockVisible(block ContentBlock, minPriority int) bool {
	if !audienceAllowsUser(block.Annotations.Audience) {
		return false
	}
	if priority := block.Annotations.Priority; priority != nil && *priority < minPriority {
		return false
	}
	return true
}

// audienceAllowsUser 判断 audience 是否允许 user 角色。缺省（nil 或空）
// 视为对所有角色可见，因此返回 true。这里只做 ASCII 精确匹配，
// 中文角色名不会误命中 "user"。
func audienceAllowsUser(audience []string) bool {
	if len(audience) == 0 {
		return true
	}
	for _, role := range audience {
		if role == contentAudienceUser {
			return true
		}
	}
	return false
}

// contentFilterMinPriority 返回 CallTool 使用的优先级过滤阈值（0-100）。
// 未设置、非数字或小于等于 0 时返回 0，表示关闭过滤。读取环境变量
// 使集成无需重新构建即可开启该可选能力。
func contentFilterMinPriority() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(contentFilterMinPriorityEnv)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
