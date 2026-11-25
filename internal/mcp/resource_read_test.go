package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestReadResourceSendsURIAndDecodesContent 固定 resources/read 的线协议形状：
// uri 以结构化参数发送，返回的 contents 原样解码给调用方。
func TestReadResourceSendsURIAndDecodesContent(t *testing.T) {
	var seen map[string]any
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"resources/read": func(raw json.RawMessage) (any, error) {
			if err := json.Unmarshal(raw, &seen); err != nil {
				return nil, err
			}
			return map[string]any{"contents": []map[string]any{
				{"uri": "file:///a.md", "mimeType": "text/markdown", "text": "你好"},
				{"uri": "file:///b.bin", "mimeType": "application/octet-stream", "blob": "AAEC"},
			}}, nil
		},
	}}
	c := newFakeClient("res", ft, time.Second)
	got, err := c.ReadResource(context.Background(), "file:///a.md")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if seen["uri"] != "file:///a.md" {
		t.Fatalf("wire uri = %v, want file:///a.md", seen["uri"])
	}
	if len(got) != 2 || got[0].Text != "你好" || got[1].Blob != "AAEC" {
		t.Fatalf("contents = %+v", got)
	}
}

// TestReadResourceEmptyURINoTransport 守护 fail-closed：空 uri 在触碰传输前被拒绝。
func TestReadResourceEmptyURINoTransport(t *testing.T) {
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){}}
	c := newFakeClient("res", ft, time.Second)
	if _, err := c.ReadResource(context.Background(), ""); err == nil {
		t.Fatal("empty uri must fail before contacting the transport")
	}
	if len(ft.notes) != 0 {
		t.Fatalf("unexpected transport activity: %v", ft.notes)
	}
}

// TestReadResourceMethodNotFoundIsError 固定：主动读取失败必须显式报错，
// 不像可选的列表能力那样降级为空列表，避免把读取失败伪装成"资源为空"。
func TestReadResourceMethodNotFoundIsError(t *testing.T) {
	ft := &fakeTransport{}
	c := newFakeClient("res", ft, time.Second)
	if _, err := c.ReadResource(context.Background(), "file:///a.md"); err == nil {
		t.Fatal("method-not-found must surface as an error")
	}
}

// TestListResourceTemplatesPagination 固定资源模板列表跟随不透明游标。
func TestListResourceTemplatesPagination(t *testing.T) {
	pages := 0
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"resources/templates/list": func(raw json.RawMessage) (any, error) {
			pages++
			var params struct {
				Cursor json.RawMessage `json:"cursor"`
			}
			_ = json.Unmarshal(raw, &params)
			if len(params.Cursor) == 0 {
				return map[string]any{
					"resourceTemplates": []map[string]any{{"uriTemplate": "file:///docs/{name}.md", "name": "doc"}},
					"nextCursor":        "p2",
				}, nil
			}
			if string(params.Cursor) != `"p2"` {
				t.Fatalf("cursor not replayed verbatim: %s", params.Cursor)
			}
			return map[string]any{
				"resourceTemplates": []map[string]any{{"uriTemplate": "file:///logs/{date}.log"}},
			}, nil
		},
	}}
	c := newFakeClient("res", ft, time.Second)
	got, err := c.ListResourceTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if pages != 2 || len(got) != 2 || got[0].URITemplate != "file:///docs/{name}.md" {
		t.Fatalf("pages=%d templates=%+v", pages, got)
	}
}
