package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestNormalizeInputSchema pins the three cleaning rules and, critically, the
// byte-for-byte guarantee for schemas that already pass them.
func TestNormalizeInputSchema(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "missing type is added",
			input: `{"properties":{"q":{"type":"string"}}}`,
			want:  `{"properties":{"q":{"type":"string"}},"type":"object"}`,
		},
		{
			name:  "null type counts as missing",
			input: `{"type":null}`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "non-object properties dropped",
			input: `{"type":"object","properties":[]}`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "null properties dropped",
			input: `{"type":"object","properties":null}`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "string required dropped",
			input: `{"type":"object","required":"name"}`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "required with a non-string element dropped",
			input: `{"type":"object","required":["a",7]}`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "null required dropped",
			input: `{"type":"object","required":null}`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "all three dirtied but other fields preserved",
			input: `{"title":"T","$schema":"x","properties":7,"required":{},"additionalProperties":false}`,
			want:  `{"$schema":"x","additionalProperties":false,"title":"T","type":"object"}`,
		},
		{
			name:  "empty input",
			input: ``,
			want:  `{"type":"object"}`,
		},
		{
			name:  "non-object root",
			input: `[]`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "invalid JSON root",
			input: `{`,
			want:  `{"type":"object"}`,
		},
		{
			name:  "clean schema keeps exact bytes",
			input: `{ "type" : "object" , "properties" : { "x" : { "type" : "string" } } , "required" : [ "x" ] , "title" : "keep" }`,
			want:  `{ "type" : "object" , "properties" : { "x" : { "type" : "string" } } , "required" : [ "x" ] , "title" : "keep" }`,
		},
		{
			name:  "clean schema without properties or required unchanged",
			input: `{"type":"object","title":"only"}`,
			want:  `{"type":"object","title":"only"}`,
		},
		{
			name:  "empty required array is valid and kept",
			input: `{"type":"object","required":[]}`,
			want:  `{"type":"object","required":[]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeInputSchema(json.RawMessage(tc.input))
			if string(got) != tc.want {
				t.Errorf("normalizeInputSchema(%s) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

// TestListToolsNormalizesAndPreservesRaw checks the decoded client path: the
// provider-facing Schema is cleaned, while the additive raw/output fields keep
// what the server sent.
func TestListToolsNormalizesAndPreservesRaw(t *testing.T) {
	// fakeTransport marshals handler output, which compacts RawMessage bytes;
	// the exact-byte guarantee is covered by TestNormalizeInputSchema instead.
	const clean = `{"type":"object","title":"keep"}`
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			return map[string]any{"tools": []any{
				map[string]any{
					"name":         "clean",
					"inputSchema":  json.RawMessage(clean),
					"outputSchema": json.RawMessage(`{"type":"object"}`),
				},
				map[string]any{
					"name":        "dirty",
					"inputSchema": json.RawMessage(`{"properties":[]}`),
				},
				map[string]any{"name": "no_schema"},
			}}, nil
		},
	}}
	c := newFakeClient("fake", ft, time.Second)

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("tool count = %d, want 3: %+v", len(tools), tools)
	}
	if string(tools[0].Schema) != clean {
		t.Errorf("clean schema changed: %s", tools[0].Schema)
	}
	if string(tools[0].RawInputSchema) != clean {
		t.Errorf("clean raw schema changed: %s", tools[0].RawInputSchema)
	}
	if string(tools[0].OutputSchema) != `{"type":"object"}` {
		t.Errorf("output schema lost: %s", tools[0].OutputSchema)
	}
	if string(tools[1].Schema) != `{"type":"object"}` {
		t.Errorf("dirty schema not cleaned: %s", tools[1].Schema)
	}
	if string(tools[1].RawInputSchema) != `{"properties":[]}` {
		t.Errorf("dirty raw schema not preserved: %s", tools[1].RawInputSchema)
	}
	if string(tools[2].Schema) != `{"type":"object"}` {
		t.Errorf("missing schema not defaulted: %s", tools[2].Schema)
	}
	if len(tools[2].RawInputSchema) != 0 {
		t.Errorf("missing raw schema should be omitted, got %s", tools[2].RawInputSchema)
	}
}

// TestListToolsSkipsMalformedEntries guards per-item tolerance: one broken
// tool must not fail the whole tools/list call.
func TestListToolsSkipsMalformedEntries(t *testing.T) {
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			return map[string]any{"tools": []any{
				map[string]any{"name": "first", "inputSchema": json.RawMessage(`{"type":"object"}`)},
				"not an object",
				123,
				map[string]any{"name": "bad annotations", "annotations": 7},
				map[string]any{"description": "nameless"},
				map[string]any{"name": "second", "inputSchema": json.RawMessage(`{"properties":{}}`)},
			}}, nil
		},
	}}
	c := newFakeClient("fake", ft, time.Second)

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("one malformed tool must not fail the table: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2: %+v", len(tools), tools)
	}
	if tools[0].Name != "first" || tools[1].Name != "second" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if string(tools[1].Schema) != `{"properties":{},"type":"object"}` {
		t.Errorf("second tool schema not cleaned: %s", tools[1].Schema)
	}
}
