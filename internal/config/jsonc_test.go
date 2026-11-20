package config

import (
	"encoding/json"
	"testing"
)

func TestStripJSONCRemovesCommentsAndTrailingCommas(t *testing.T) {
	input := `{
  // a line comment with "quotes" and { braces }
  "nested": [1, 2, 3,],
  "trailing": {"a": true,}, /* block comment */
  "last": false,
}`
	cleaned := StripJSONC(input)
	var got map[string]any
	if err := json.Unmarshal([]byte(cleaned), &got); err != nil {
		t.Fatalf("cleaned JSON is invalid: %v\n%s", err, cleaned)
	}
	nested, ok := got["nested"].([]any)
	if !ok || len(nested) != 3 {
		t.Errorf("nested = %#v, want [1 2 3]", got["nested"])
	}
	trailing, ok := got["trailing"].(map[string]any)
	if !ok || trailing["a"] != true {
		t.Errorf("trailing = %#v, want {a:true}", got["trailing"])
	}
	if got["last"] != false {
		t.Errorf("last = %#v, want false", got["last"])
	}
}

func TestStripJSONCPreservesCommentMarkersInsideStrings(t *testing.T) {
	input := `{
  "url": "http://example.com/a/*b",
  "note": "say \"// not a comment\" and /* also not */",
  "block": "text /* inside */ text",
  "hash": "#//",
}`
	cleaned := StripJSONC(input)
	var got map[string]any
	if err := json.Unmarshal([]byte(cleaned), &got); err != nil {
		t.Fatalf("cleaned JSON is invalid: %v\n%s", err, cleaned)
	}
	want := map[string]string{
		"url":   "http://example.com/a/*b",
		"note":  `say "// not a comment" and /* also not */`,
		"block": "text /* inside */ text",
		"hash":  "#//",
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s = %q, want %q", key, got[key], expected)
		}
	}
}

func TestStripJSONCTrailingCommaBeforeCommentedCloser(t *testing.T) {
	input := `{"a":1,// explain the close
/* and a block */ }`
	cleaned := StripJSONC(input)
	var got map[string]any
	if err := json.Unmarshal([]byte(cleaned), &got); err != nil {
		t.Fatalf("cleaned JSON is invalid: %v\n%s", err, cleaned)
	}
	if got["a"] != float64(1) {
		t.Errorf("a = %#v, want 1", got["a"])
	}
}

func TestStripJSONCEscapedQuoteDoesNotCloseString(t *testing.T) {
	input := `{"text":"a\\\"//still string", "ok":1}`
	cleaned := StripJSONC(input)
	var got map[string]any
	if err := json.Unmarshal([]byte(cleaned), &got); err != nil {
		t.Fatalf("cleaned JSON is invalid: %v\n%s", err, cleaned)
	}
	// 该值是一个反斜杠后跟一个引号，然后是字符串内的 //。
	if got["text"] != `a\"//still string` {
		t.Errorf("text = %q, want %q", got["text"], `a\"//still string`)
	}
}

func TestStripJSONCStripsUTF8BOM(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"bom before object", "\ufeff" + `{"a":1}`, `{"a":1}`},
		{"bom only", "\ufeff", ""},
		{"no bom", `{"a":1}`, `{"a":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripJSONC(tc.input); got != tc.want {
				t.Errorf("StripJSONC(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
