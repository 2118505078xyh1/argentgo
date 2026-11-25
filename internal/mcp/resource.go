package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// ResourceTemplate 是服务器宣告的一个资源模板（uriTemplate 可带变量）。
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

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

// ListResourceTemplates 返回服务器的资源模板。未实现该方法的服务器返回空列表，
// 与 ListResources 的宽松语义一致，不让可选能力导致整台服务器不可用。
func (c *client) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	if c.capabilitySkipped("resources") {
		return nil, nil
	}
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	templates := make([]ResourceTemplate, 0)
	err := c.forEachListPage(ctx, "resources/templates/list", func(raw json.RawMessage) (json.RawMessage, error) {
		var result struct {
			Templates []ResourceTemplate `json:"resourceTemplates"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("mcp %q: decode resources/templates/list result: %w", c.spec.Name, err)
		}
		templates = append(templates, result.Templates...)
		// 传入整个响应：nextResourceCursor 与列表分页共用同一套游标提取语义。
		return nextResourceCursor(raw), nil
	})
	if err != nil {
		if methodNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return templates, nil
}

// ReadResource 按 URI 读取一个资源。uri 非空由 resourceReadParams 前置校验，
// 因此不会为无效输入触碰传输层。返回的字节原样交给调用方，截断与脱敏属于
// 消费侧职责（模型可见内容必须先在 app 层限幅）。
func (c *client) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	params := resourceReadParams(uri, nil)
	if params == nil {
		return nil, fmt.Errorf("mcp %q: resources/read requires a non-empty uri", c.spec.Name)
	}
	ctx, cancel := c.timeoutContext(ctx)
	defer cancel()

	raw, err := c.callWithRetry(ctx, "resources/read", params)
	if err != nil {
		return nil, c.wrap(err, "resources/read")
	}
	var result struct {
		Contents []ResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp %q: decode resources/read result: %w", c.spec.Name, err)
	}
	return result.Contents, nil
}
