package mcp

import "encoding/json"

// resourceReadParams 构造 resources/read 的请求参数：必填的 uri，以及可选的
// 不透明游标。uri 为空时返回 nil，调用方据此在触碰传输之前拒绝无效请求。
//
// uri 和游标都作为结构化值放进 map，由 encoding/json 统一负责转义，因此
// CJK 字符、引号、反斜杠或控制字符都不会因手工拼接而破坏 JSON 结构。
// 游标沿用列表分页的约定：原样透传字节，不强制转换成字符串。
func resourceReadParams(uri string, cursor json.RawMessage) map[string]any {
	if uri == "" {
		return nil
	}
	params := map[string]any{"uri": uri}
	if len(cursor) > 0 {
		params["cursor"] = cursor
	}
	return params
}

// nextResourceCursor 从 resources/list 的响应中提取下一页游标。它复用
// nextCursor 的空值判定，让资源列表和其他列表遵守同一套终止规则；
// 无法解码的响应视为没有更多页。
func nextResourceCursor(raw json.RawMessage) json.RawMessage {
	var result struct {
		Next json.RawMessage `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	return nextCursor(result.Next)
}
