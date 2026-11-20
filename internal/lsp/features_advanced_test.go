package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestAdvancedMethodsAgainstFakeServer(t *testing.T) {
	span := ideRange(0, 2, 0, 7)
	command := &Command{Title: "Run tests", Command: "go.test"}
	commandObj := map[string]any{"title": command.Title, "command": command.Command}
	data := []any{0, 2, 3, 5, 0, 0, 4, 2, 12, 0}

	cases := []struct {
		name     string
		method   string
		result   any
		want     any
		nullWant any
		call     func(*Client) (any, error)
	}{
		{
			name:     "SemanticTokens",
			method:   "textDocument/semanticTokens/full",
			result:   map[string]any{"resultId": "v1", "data": data},
			want:     &SemanticTokens{ResultID: "v1", Data: []uint32{0, 2, 3, 5, 0, 0, 4, 2, 12, 0}},
			nullWant: (*SemanticTokens)(nil),
			call: ideCall(func(ctx context.Context, c *Client) (*SemanticTokens, error) {
				return c.SemanticTokens(ctx, "file:///x.go")
			}),
		},
		{
			name:   "InlayHints",
			method: "textDocument/inlayHint",
			result: []any{map[string]any{
				"position":     map[string]any{"line": 0, "character": 2},
				"label":        "i: int",
				"kind":         3,
				"tooltip":      "type of i",
				"paddingLeft":  true,
				"paddingRight": false,
			}},
			want: []InlayHint{{
				Position: Human(1, 3), Label: "i: int", Kind: 3,
				Tooltip: "type of i", PaddingLeft: true,
			}},
			nullWant: []InlayHint(nil),
			call: ideCall(func(ctx context.Context, c *Client) ([]InlayHint, error) {
				return c.InlayHints(ctx, "file:///x.go", span)
			}),
		},
		{
			name:   "CodeLens",
			method: "textDocument/codeLens",
			result: []any{
				map[string]any{"range": rangeMap(span), "command": commandObj},
				map[string]any{"range": rangeMap(span)},
			},
			want: []CodeLens{
				{Range: span, Command: command, IsResolved: true},
				{Range: span, IsResolved: false},
			},
			nullWant: []CodeLens(nil),
			call: ideCall(func(ctx context.Context, c *Client) ([]CodeLens, error) {
				return c.CodeLens(ctx, "file:///x.go")
			}),
		},
		{
			name:     "ResolveCodeLens",
			method:   "codeLens/resolve",
			result:   map[string]any{"range": rangeMap(span), "command": commandObj},
			want:     &CodeLens{Range: span, Command: command, IsResolved: true},
			nullWant: (*CodeLens)(nil),
			call: ideCall(func(ctx context.Context, c *Client) (*CodeLens, error) {
				return c.ResolveCodeLens(ctx, CodeLens{Range: span})
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			okClient := newIDEFakeClient(t, func(method string, _ json.RawMessage) (any, *rpcError) {
				if method == tc.method {
					return tc.result, nil
				}
				return nil, nil
			})
			got, err := tc.call(okClient)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s = %#v, want %#v", tc.name, got, tc.want)
			}

			nullClient := newIDEFakeClient(t, func(method string, _ json.RawMessage) (any, *rpcError) {
				if method == tc.method {
					return nil, nil
				}
				return nil, nil
			})
			got, err = tc.call(nullClient)
			if err != nil {
				t.Fatalf("%s with null result: %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.nullWant) {
				t.Fatalf("%s with null result = %#v, want %#v", tc.name, got, tc.nullWant)
			}

			message := "kaboom " + tc.method
			errClient := newIDEFakeClient(t, func(method string, _ json.RawMessage) (any, *rpcError) {
				if method == tc.method {
					return nil, &rpcError{Code: -32000, Message: message}
				}
				return nil, nil
			})
			_, err = tc.call(errClient)
			if err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("%s error = %v, want propagated server error %q", tc.name, err, message)
			}
		})
	}
}

func TestAdvancedMethodsSendExpectedParams(t *testing.T) {
	var mu sync.Mutex
	got := make(map[string]json.RawMessage)
	client := newIDEFakeClient(t, func(method string, params json.RawMessage) (any, *rpcError) {
		mu.Lock()
		got[method] = params
		mu.Unlock()
		return nil, nil
	})
	ctx := ideCtx(t)
	span := ideRange(1, 2, 3, 4)

	if _, err := client.SemanticTokens(ctx, "file:///x.go"); err != nil {
		t.Fatalf("SemanticTokens: %v", err)
	}
	if _, err := client.InlayHints(ctx, "file:///x.go", span); err != nil {
		t.Fatalf("InlayHints: %v", err)
	}
	if _, err := client.CodeLens(ctx, "file:///x.go"); err != nil {
		t.Fatalf("CodeLens: %v", err)
	}
	lens := CodeLens{Range: span, Command: &Command{Title: "Run", Command: "go.test"}}
	if _, err := client.ResolveCodeLens(ctx, lens); err != nil {
		t.Fatalf("ResolveCodeLens: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	type document struct {
		URI string `json:"uri"`
	}
	var tokens struct {
		TextDocument document `json:"textDocument"`
	}
	if err := json.Unmarshal(got["textDocument/semanticTokens/full"], &tokens); err != nil {
		t.Fatalf("semanticTokens params: %v", err)
	}
	if tokens.TextDocument.URI != "file:///x.go" {
		t.Fatalf("semanticTokens uri = %q", tokens.TextDocument.URI)
	}

	var hints struct {
		TextDocument document `json:"textDocument"`
		Range        Range    `json:"range"`
	}
	if err := json.Unmarshal(got["textDocument/inlayHint"], &hints); err != nil {
		t.Fatalf("inlayHint params: %v", err)
	}
	if hints.TextDocument.URI != "file:///x.go" || !reflect.DeepEqual(hints.Range, span) {
		t.Fatalf("inlayHint params = %#v, want uri file:///x.go and range %#v", hints, span)
	}

	var lenses struct {
		TextDocument document `json:"textDocument"`
	}
	if err := json.Unmarshal(got["textDocument/codeLens"], &lenses); err != nil {
		t.Fatalf("codeLens params: %v", err)
	}
	if lenses.TextDocument.URI != "file:///x.go" {
		t.Fatalf("codeLens uri = %q", lenses.TextDocument.URI)
	}

	var resolved struct {
		Range   Range    `json:"range"`
		Command *Command `json:"command"`
	}
	if err := json.Unmarshal(got["codeLens/resolve"], &resolved); err != nil {
		t.Fatalf("codeLens/resolve params: %v", err)
	}
	if !reflect.DeepEqual(resolved.Range, span) || resolved.Command == nil || resolved.Command.Command != "go.test" {
		t.Fatalf("codeLens/resolve params = %#v, want range %#v and command go.test", resolved, span)
	}
}

func TestAdvancedAtVariantsOpenPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, path, "package main\n")
	wantURI := PathToURI(path)

	var mu sync.Mutex
	got := make(map[string]string)
	client := newIDEFakeClient(t, func(method string, params json.RawMessage) (any, *rpcError) {
		var payload struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(params, &payload); err == nil {
			mu.Lock()
			got[method] = payload.TextDocument.URI
			mu.Unlock()
		}
		switch method {
		case "textDocument/semanticTokens/full":
			return map[string]any{"data": []any{}}, nil
		default:
			return []any{}, nil
		}
	})
	ctx := ideCtx(t)

	if _, err := client.SemanticTokensAt(ctx, path); err != nil {
		t.Fatalf("SemanticTokensAt: %v", err)
	}
	if _, err := client.InlayHintsAt(ctx, path, ideRange(0, 0, 1, 0)); err != nil {
		t.Fatalf("InlayHintsAt: %v", err)
	}
	if _, err := client.CodeLensAt(ctx, path); err != nil {
		t.Fatalf("CodeLensAt: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, method := range []string{
		"textDocument/semanticTokens/full",
		"textDocument/inlayHint",
		"textDocument/codeLens",
	} {
		if got[method] != wantURI {
			t.Fatalf("%s opened %q, want %q", method, got[method], wantURI)
		}
	}
}

func TestParseSemanticTokensPreservesRawStream(t *testing.T) {
	want := &SemanticTokens{ResultID: "v1", Data: []uint32{0, 2, 3, 5, 0}}
	raw := mustJSON(t, map[string]any{"resultId": "v1", "data": []any{0, 2, 3, 5, 0}})

	if got := parseSemanticTokens(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSemanticTokens(object) = %#v, want %#v", got, want)
	}
	if got := parseSemanticTokens(mustJSON(t, []any{map[string]any{"resultId": "v1", "data": []any{0, 2, 3, 5, 0}}})); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSemanticTokens(wrapped array) = %#v, want %#v", got, want)
	}
	if got := parseSemanticTokens(mustJSON(t, map[string]any{"resultId": "v2"})); !reflect.DeepEqual(got, &SemanticTokens{ResultID: "v2"}) {
		t.Fatalf("parseSemanticTokens(missing data) = %#v", got)
	}
	// A malformed data element leaves an empty stream, not a failed request.
	if got := parseSemanticTokens(mustJSON(t, map[string]any{"data": []any{"bad"}})); got == nil || len(got.Data) != 0 {
		t.Fatalf("parseSemanticTokens(malformed data) = %#v, want empty stream", got)
	}
}

func TestParseInlayHintsAcceptsArraySingleAndSkipsMalformed(t *testing.T) {
	first := InlayHint{
		Position: Human(1, 3), Label: "i: int", Kind: 3,
		Tooltip: "type", PaddingLeft: true,
	}
	span := ideRange(2, 0, 2, 4)
	second := InlayHint{Position: span.Start, Label: "n\n\nx"}
	entries := []any{
		map[string]any{
			"position":    map[string]any{"line": 0, "character": 2},
			"label":       "i: int",
			"kind":        3,
			"tooltip":     map[string]any{"kind": "markdown", "value": "type"},
			"paddingLeft": true,
		},
		map[string]any{
			"position": map[string]any{"line": 2, "character": 0},
			"label":    []any{map[string]any{"value": "n"}, map[string]any{"value": "x"}},
		},
		42,               // malformed: skipped
		nil,              // null entry: skipped
		map[string]any{}, // missing fields are tolerated
	}

	want := []InlayHint{first, second, {}}
	if got := parseInlayHints(mustJSON(t, entries)); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseInlayHints(array) = %#v, want %#v", got, want)
	}
	if got := parseInlayHints(mustJSON(t, entries[0])); !reflect.DeepEqual(got, []InlayHint{first}) {
		t.Fatalf("parseInlayHints(single) = %#v, want %#v", got, []InlayHint{first})
	}
}

func TestParseCodeLensAcceptsArraySingleAndSkipsMalformed(t *testing.T) {
	span := ideRange(0, 2, 0, 7)
	command := &Command{Title: "Run", Command: "go.test"}
	commandObj := map[string]any{"title": command.Title, "command": command.Command}
	lensObj := map[string]any{"range": rangeMap(span), "command": commandObj}
	pendingObj := map[string]any{"range": rangeMap(span)}
	want := []CodeLens{
		{Range: span, Command: command, IsResolved: true},
		{Range: span, IsResolved: false},
	}

	entries := []any{lensObj, pendingObj, 42, nil, map[string]any{}}
	if got := parseCodeLens(mustJSON(t, entries)); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCodeLens(array) = %#v, want %#v", got, want)
	}
	if got := parseCodeLens(mustJSON(t, lensObj)); !reflect.DeepEqual(got, want[:1]) {
		t.Fatalf("parseCodeLens(single) = %#v, want %#v", got, want[:1])
	}
}

func TestAdvancedParseHelpersReturnNilForNullOrMalformed(t *testing.T) {
	parsers := []struct {
		name  string
		parse func(json.RawMessage) any
	}{
		{"parseSemanticTokens", func(raw json.RawMessage) any { return parseSemanticTokens(raw) }},
		{"parseInlayHints", func(raw json.RawMessage) any { return parseInlayHints(raw) }},
		{"parseCodeLens", func(raw json.RawMessage) any { return parseCodeLens(raw) }},
	}
	inputs := []struct {
		name string
		raw  json.RawMessage
	}{
		{"null", json.RawMessage("null")},
		{"empty", json.RawMessage("")},
		{"empty array", json.RawMessage(`[]`)},
		{"scalar", json.RawMessage(`42`)},
		{"malformed object", json.RawMessage(`{bad`)},
	}
	for _, parser := range parsers {
		for _, input := range inputs {
			t.Run(parser.name+"/"+input.name, func(t *testing.T) {
				got := parser.parse(input.raw)
				value := reflect.ValueOf(got)
				if !value.IsValid() || (value.Kind() != reflect.Ptr && value.Kind() != reflect.Slice) || !value.IsNil() {
					t.Fatalf("%s(%s) = %#v, want nil", parser.name, input.name, got)
				}
			})
		}
	}
}
