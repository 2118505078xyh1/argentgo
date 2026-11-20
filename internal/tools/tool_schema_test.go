package tools

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestProjectToolSchema(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		compat ModelCompat
		want   string
		exact  bool // compare raw bytes instead of JSON semantics
	}{
		{
			name:   "none leaves normal schema byte-identical",
			input:  `{"type":"object","properties":{"path":{"type":"string"}}}`,
			compat: CompatNone,
			want:   `{"type":"object","properties":{"path":{"type":"string"}}}`,
			exact:  true,
		},
		{
			name:   "gemini leaves existing object root byte-identical",
			input:  `{"type":"object","properties":{"path":{"type":"string"}}}`,
			compat: CompatGemini,
			want:   `{"type":"object","properties":{"path":{"type":"string"}}}`,
			exact:  true,
		},
		{
			name:   "moonshot leaves typed enums byte-identical",
			input:  `{"type":"object","properties":{"format":{"type":"string","enum":["rfc3339","unix"]}}}`,
			compat: CompatMoonshot,
			want:   `{"type":"object","properties":{"format":{"type":"string","enum":["rfc3339","unix"]}}}`,
			exact:  true,
		},
		{
			name:   "gemini empty schema gains object root",
			input:  `{}`,
			compat: CompatGemini,
			want:   `{"type":"object"}`,
		},
		{
			name:   "gemini not-null guard gains object root",
			input:  `{"not":{"type":"null"}}`,
			compat: CompatGemini,
			want:   `{"not":{"type":"null"},"type":"object"}`,
		},
		{
			name:   "gemini bare anyOf gains object root",
			input:  `{"anyOf":[{"required":["command"]},{"required":["script"]}]}`,
			compat: CompatGemini,
			want:   `{"anyOf":[{"required":["command"]},{"required":["script"]}],"type":"object"}`,
		},
		{
			name:   "gemini does not override an existing root type",
			input:  `{"type":"string"}`,
			compat: CompatGemini,
			want:   `{"type":"string"}`,
			exact:  true,
		},
		{
			name:   "moonshot types an untyped string enum at the root",
			input:  `{"enum":["a","b"]}`,
			compat: CompatMoonshot,
			want:   `{"enum":["a","b"],"type":"string"}`,
		},
		{
			name:   "moonshot types an untyped enum in a nested property",
			input:  `{"type":"object","properties":{"status":{"enum":["pending","done"]}}}`,
			compat: CompatMoonshot,
			want:   `{"type":"object","properties":{"status":{"enum":["pending","done"],"type":"string"}}}`,
		},
		{
			name:   "moonshot recurses into array items",
			input:  `{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"status":{"enum":["pending","completed"]}}}}}}`,
			compat: CompatMoonshot,
			want:   `{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"status":{"enum":["pending","completed"],"type":"string"}}}}}}`,
		},
		{
			name:   "moonshot recurses into additionalProperties",
			input:  `{"type":"object","additionalProperties":{"enum":["x","y"]}}`,
			compat: CompatMoonshot,
			want:   `{"type":"object","additionalProperties":{"enum":["x","y"],"type":"string"}}`,
		},
		{
			name:   "moonshot infers integer for a numeric enum",
			input:  `{"enum":[1,2,3]}`,
			compat: CompatMoonshot,
			want:   `{"enum":[1,2,3],"type":"integer"}`,
		},
		{
			name:   "moonshot skips an enum with mixed value types",
			input:  `{"enum":["a",1]}`,
			compat: CompatMoonshot,
			want:   `{"enum":["a",1]}`,
			exact:  true,
		},
		{
			name:   "moonshot skips an enum that already declares a type",
			input:  `{"type":"integer","enum":[1,2]}`,
			compat: CompatMoonshot,
			want:   `{"type":"integer","enum":[1,2]}`,
			exact:  true,
		},
		{
			name:   "all compat combines object root and enum typing",
			input:  `{"anyOf":[{"enum":["a"]}]}`,
			compat: CompatAll,
			want:   `{"anyOf":[{"enum":["a"],"type":"string"}],"type":"object"}`,
		},
		{
			name:   "invalid JSON is returned as-is",
			input:  `{"type":"object",`,
			compat: CompatAll,
			want:   `{"type":"object",`,
			exact:  true,
		},
		{
			name:   "empty input is returned as-is",
			input:  ``,
			compat: CompatAll,
			want:   ``,
			exact:  true,
		},
		{
			name:   "non-UTF-8 input is returned as-is",
			input:  "{\"description\":\"" + string([]byte{0xff, 0xfe}) + "\"}",
			compat: CompatAll,
			want:   "{\"description\":\"" + string([]byte{0xff, 0xfe}) + "\"}",
			exact:  true,
		},
		{
			name:   "UTF-8 content survives projection",
			input:  `{"properties":{"模式":{"enum":["快速","慢速"]}}}`,
			compat: CompatMoonshot,
			want:   `{"properties":{"模式":{"enum":["快速","慢速"],"type":"string"}}}`,
			exact:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProjectToolSchema(tt.input, tt.compat)
			if tt.exact {
				if got != tt.want {
					t.Fatalf("ProjectToolSchema() = %q, want exact %q", got, tt.want)
				}
			} else {
				assertSchemaJSONEqual(t, got, tt.want)
			}
			// Determinism: the same input must always produce the same output.
			if again := ProjectToolSchema(tt.input, tt.compat); again != got {
				t.Fatalf("non-deterministic result: first %q, second %q", got, again)
			}
		})
	}
}

func assertSchemaJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("projected schema is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("test expectation is not valid JSON: %v (%s)", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("schema mismatch\n got: %s\nwant: %s", got, want)
	}
}
