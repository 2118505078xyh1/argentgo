package mcp

import (
	"encoding/json"
	"testing"
)

// TestResourceReadParamsBuildsURI 检查基本参数构造：只有 uri，没有游标。
func TestResourceReadParamsBuildsURI(t *testing.T) {
	params := resourceReadParams("file:///a/b.txt", nil)
	if params == nil {
		t.Fatal("expected params for non-empty uri")
	}
	if len(params) != 1 {
		t.Fatalf("got %d params, want 1: %+v", len(params), params)
	}
	if got, ok := params["uri"].(string); !ok || got != "file:///a/b.txt" {
		t.Fatalf("uri param = %v, want %q", params["uri"], "file:///a/b.txt")
	}
	if _, hasCursor := params["cursor"]; hasCursor {
		t.Fatalf("cursor must be omitted when absent, got %+v", params)
	}
}

// TestResourceReadParamsReplaysCursor 检查游标被原样放进参数，不强制转换类型。
func TestResourceReadParamsReplaysCursor(t *testing.T) {
	cases := []struct {
		name   string
		cursor json.RawMessage
		want   string
	}{
		{"string", json.RawMessage(`"page-2"`), `"page-2"`},
		{"number", json.RawMessage(`42`), `42`},
		{"object", json.RawMessage(`{"offset":7}`), `{"offset":7}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := resourceReadParams("file:///a", tc.cursor)
			if params == nil {
				t.Fatal("expected params for non-empty uri")
			}
			got, ok := params["cursor"].(json.RawMessage)
			if !ok {
				t.Fatalf("cursor param type = %T, want json.RawMessage", params["cursor"])
			}
			if string(got) != tc.want {
				t.Fatalf("cursor = %s, want %s", got, tc.want)
			}
			if params["uri"] != "file:///a" {
				t.Fatalf("uri must survive cursor addition, got %v", params["uri"])
			}
		})
	}
}

// TestResourceReadParamsEmptyURI 守护 fail-closed 约定：空的 uri 不产生任何参数。
func TestResourceReadParamsEmptyURI(t *testing.T) {
	if params := resourceReadParams("", nil); params != nil {
		t.Fatalf("empty uri must return nil params, got %+v", params)
	}
	if params := resourceReadParams("", json.RawMessage(`"c"`)); params != nil {
		t.Fatalf("empty uri must return nil params even with cursor, got %+v", params)
	}
}

// TestResourceReadParamsCJKURISafe 确认 CJK、引号、反斜杠和尖括号由
// encoding/json 正确转义， marshal 后仍是合法 JSON 且 uri 可无损往返。
func TestResourceReadParamsCJKURISafe(t *testing.T) {
	uri := `file:///文档/2026 年度报告（草稿）.md?q=你好&x=<标签>&q2="引用"&path=C:\资料`
	params := resourceReadParams(uri, json.RawMessage(`"页-2"`))
	if params == nil {
		t.Fatal("expected params for non-empty CJK uri")
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("params are not valid JSON: %s", raw)
	}
	var decoded struct {
		URI    string          `json:"uri"`
		Cursor json.RawMessage `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if decoded.URI != uri {
		t.Fatalf("uri round-trip = %q, want %q", decoded.URI, uri)
	}
	if string(decoded.Cursor) != `"页-2"` {
		t.Fatalf("cursor round-trip = %s, want %s", decoded.Cursor, `"页-2"`)
	}
}

// TestNextResourceCursor 覆盖资源游标提取的空值与不透明形态。
func TestNextResourceCursor(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"nil", nil, ""},
		{"absent", json.RawMessage(`{}`), ""},
		{"null", json.RawMessage(`{"nextCursor":null}`), ""},
		{"empty string", json.RawMessage(`{"nextCursor":""}`), ""},
		{"string", json.RawMessage(`{"nextCursor":"abc"}`), `"abc"`},
		{"number", json.RawMessage(`{"nextCursor":42}`), `42`},
		{"object", json.RawMessage(`{"nextCursor":{"offset":7}}`), `{"offset":7}`},
		{"with resources", json.RawMessage(`{"resources":[],"nextCursor":"p2"}`), `"p2"`},
		{"invalid json", json.RawMessage(`not json`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextResourceCursor(tc.raw)
			if string(got) != tc.want {
				t.Fatalf("nextResourceCursor(%s) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}
