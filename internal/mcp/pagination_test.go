package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// cursorParam 读取分页列表请求发送的不透明游标。第一个
// 请求完全没有 params（nil），因此缺失的成员表示"第一
// 页"。
func cursorParam(params json.RawMessage) json.RawMessage {
	if len(params) == 0 {
		return nil
	}
	var req struct {
		Cursor json.RawMessage `json:"cursor"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil
	}
	return req.Cursor
}

func pageTool(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"inputSchema": map[string]any{"type": "object"},
	}
}

// TestListToolsFollowsNextCursor 守护核心分页缺陷：必须收集第一页
// 之后的工具，且每个后续请求都必须携带服务器
// 交回的确切 token。
func TestListToolsFollowsNextCursor(t *testing.T) {
	var requests []string
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(params json.RawMessage) (any, error) {
			cursor := cursorParam(params)
			requests = append(requests, string(cursor))
			switch string(cursor) {
			case "":
				return map[string]any{
					"tools":      []any{pageTool("one")},
					"nextCursor": "page-2",
				}, nil
			case `"page-2"`:
				return map[string]any{
					"tools":      []any{pageTool("two"), pageTool("three")},
					"nextCursor": "page-3",
				}, nil
			case `"page-3"`:
				// 空 token 结束链条。
				return map[string]any{
					"tools":      []any{pageTool("four")},
					"nextCursor": "",
				}, nil
			default:
				return nil, fmt.Errorf("unexpected cursor %s", cursor)
			}
		},
	}}

	tools, err := newFakeClient("srv", ft, time.Second).ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := []string{"one", "two", "three", "four"}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d: %+v", len(tools), len(want), tools)
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Fatalf("tools[%d] = %q, want %q", i, tools[i].Name, name)
		}
	}
	// 第一次调用必须保持旧的无 params 形态；后续调用原样重放
	// token（包括其 JSON 字符串引号）。
	wantReq := []string{"", `"page-2"`, `"page-3"`}
	if len(requests) != len(wantReq) {
		t.Fatalf("got %d requests, want %d: %v", len(requests), len(wantReq), requests)
	}
	for i, req := range wantReq {
		if requests[i] != req {
			t.Fatalf("request[%d] cursor = %q, want %q", i, requests[i], req)
		}
	}
}

// TestListResourcesFollowsNextCursor 把同样的分页规则应用到
// resources/list。
func TestListResourcesFollowsNextCursor(t *testing.T) {
	var requests []string
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"resources/list": func(params json.RawMessage) (any, error) {
			cursor := cursorParam(params)
			requests = append(requests, string(cursor))
			switch string(cursor) {
			case "":
				return map[string]any{
					"resources":  []any{map[string]any{"uri": "file:///a"}},
					"nextCursor": "r2",
				}, nil
			case `"r2"`:
				return map[string]any{
					"resources": []any{map[string]any{"uri": "file:///b"}},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected cursor %s", cursor)
			}
		},
	}}

	resources, err := newFakeClient("srv", ft, time.Second).ListResources(context.Background())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 2 || resources[0].URI != "file:///a" || resources[1].URI != "file:///b" {
		t.Fatalf("unexpected resources: %+v", resources)
	}
	if len(requests) != 2 || requests[0] != "" || requests[1] != `"r2"` {
		t.Fatalf("unexpected cursor requests: %v", requests)
	}
}

// TestListToolsReplaysOpaqueCursor 检查非字符串 token 是否被
// 原样发回，而不是被强制转换或丢弃。
func TestListToolsReplaysOpaqueCursor(t *testing.T) {
	var second string
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(params json.RawMessage) (any, error) {
			cursor := cursorParam(params)
			if len(cursor) == 0 {
				return map[string]any{
					"tools":      []any{pageTool("first")},
					"nextCursor": json.RawMessage(`{"offset":42}`),
				}, nil
			}
			second = string(cursor)
			return map[string]any{"tools": []any{pageTool("second")}}, nil
		},
	}}

	tools, err := newFakeClient("srv", ft, time.Second).ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2: %+v", len(tools), tools)
	}
	if second != `{"offset":42}` {
		t.Fatalf("cursor not replayed verbatim: %s", second)
	}
}

// TestListToolsStopsOnRepeatedCursor 确保不断发回
// 相同 token 的服务器无法让客户端永远旋转，且失败的分页
// 运行不会留下部分缓存的工具视图。
func TestListToolsStopsOnRepeatedCursor(t *testing.T) {
	calls := 0
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			calls++
			return map[string]any{
				"tools":      []any{pageTool("loop")},
				"nextCursor": "same",
			}, nil
		},
	}}
	c := newFakeClient("srv", ft, time.Second)

	if _, err := c.ListTools(context.Background()); err == nil || !strings.Contains(err.Error(), "repeated pagination cursor") {
		t.Fatalf("expected a repeated-cursor error, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("got %d calls, want 2 (first page + one repeat)", calls)
	}
	if cached := c.Tools(); len(cached) != 0 {
		t.Fatalf("failed pagination must not cache a partial view: %+v", cached)
	}
}

// TestNextCursorToken 覆盖终止分页的空 token 形态，以及
// 必须保留的不透明形态。
func TestNextCursorToken(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"absent", nil, ""},
		{"null", json.RawMessage(`null`), ""},
		{"empty string", json.RawMessage(`""`), ""},
		{"whitespace", json.RawMessage("  \n "), ""},
		{"string", json.RawMessage(`"abc"`), `"abc"`},
		{"number", json.RawMessage(`42`), `42`},
		{"object", json.RawMessage(`{"k":1}`), `{"k":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextCursor(tc.raw)
			if string(got) != tc.want {
				t.Fatalf("nextCursor(%s) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}
