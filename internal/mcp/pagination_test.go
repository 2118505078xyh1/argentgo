package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// cursorParam reads the opaque cursor sent by a paginated list request. The
// first request has no params at all (nil), so an absent member means "first
// page".
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

// TestListToolsFollowsNextCursor guards the core pagination bug: tools beyond
// the first page must be collected, and each follow-up request must carry the
// exact token the server handed back.
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
				// An empty token ends the chain.
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
	// The first call must keep the legacy no-params shape; follow-ups replay
	// the token verbatim (including its JSON string quoting).
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

// TestListResourcesFollowsNextCursor applies the same paging rule to
// resources/list.
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

// TestListToolsReplaysOpaqueCursor checks that a non-string token is sent back
// untouched instead of being coerced or dropped.
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

// TestListToolsStopsOnRepeatedCursor ensures a server that keeps handing back
// the same token cannot spin the client forever, and that a failed pagination
// run leaves no partially cached tool view.
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

// TestNextCursorToken covers the empty-token shapes that terminate paging and
// the opaque shapes that must be preserved.
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
