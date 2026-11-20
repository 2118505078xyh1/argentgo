package llm

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

func TestFlattenToolSchema(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		exact bool // compare raw bytes instead of JSON semantics
	}{
		{
			name:  "invalid JSON is returned as-is",
			input: `{"allOf":[`,
			want:  `{"allOf":[`,
			exact: true,
		},
		{
			name:  "non-UTF-8 input is returned as-is",
			input: "{\"description\":\"" + string([]byte{0xff, 0xfe}) + "\"}",
			want:  "{\"description\":\"" + string([]byte{0xff, 0xfe}) + "\"}",
			exact: true,
		},
		{
			name:  "schema without allOf or sentinel is byte-identical",
			input: `{"type":"object","properties":{"a":{"type":"string"}}}`,
			want:  `{"type":"object","properties":{"a":{"type":"string"}}}`,
			exact: true,
		},
		{
			name:  "allOf members merge into the parent",
			input: `{"allOf":[{"type":"object"},{"properties":{"a":{"type":"string"}}}]}`,
			want:  `{"type":"object","properties":{"a":{"type":"string"}}}`,
		},
		{
			name:  "parent keys survive the merge",
			input: `{"title":"root","allOf":[{"description":"child","required":["a"]}]}`,
			want:  `{"title":"root","description":"child","required":["a"]}`,
		},
		{
			name:  "required is preserved through the merge",
			input: `{"allOf":[{"properties":{"a":{"type":"string"}}},{"required":["a"]}]}`,
			want:  `{"properties":{"a":{"type":"string"}},"required":["a"]}`,
		},
		{
			name:  "effect-style integer constraint merges",
			input: `{"type":"integer","allOf":[{"minimum":0}]}`,
			want:  `{"type":"integer","minimum":0}`,
		},
		{
			name:  "JS safe-integer sentinel is removed",
			input: `{"type":"integer","minimum":-9007199254740991,"maximum":9007199254740991}`,
			want:  `{"type":"integer"}`,
		},
		{
			name:  "int64/3 safe-integer sentinel is removed",
			input: `{"type":"integer","minimum":-3074457345618258603,"maximum":3074457345618258602}`,
			want:  `{"type":"integer"}`,
		},
		{
			name:  "exponent-form sentinel is removed",
			input: `{"type":"integer","minimum":-9.007199254740991e15,"maximum":9.007199254740991e15}`,
			want:  `{"type":"integer"}`,
		},
		{
			name:  "real bounds near the sentinel are kept",
			input: `{"type":"integer","minimum":0,"maximum":9007199254740990}`,
			want:  `{"type":"integer","minimum":0,"maximum":9007199254740990}`,
			exact: true,
		},
		{
			name:  "sentinel bounds on a non-integer schema are kept",
			input: `{"type":"number","minimum":-9007199254740991,"maximum":9007199254740991}`,
			want:  `{"type":"number","minimum":-9007199254740991,"maximum":9007199254740991}`,
			exact: true,
		},
		{
			name:  "conflicting keys in members keep allOf",
			input: `{"allOf":[{"type":"object"},{"type":"string"}]}`,
			want:  `{"allOf":[{"type":"object"},{"type":"string"}]}`,
			exact: true,
		},
		{
			name:  "parent/member key conflict keeps allOf",
			input: `{"type":"object","allOf":[{"properties":{"a":{}}},{"type":"string"}]}`,
			want:  `{"type":"object","allOf":[{"properties":{"a":{}}},{"type":"string"}]}`,
			exact: true,
		},
		{
			name:  "sentinel inside an unmergeable allOf child is still removed",
			input: `{"type":"object","allOf":[{"type":"integer","minimum":-9007199254740991,"maximum":9007199254740991}]}`,
			want:  `{"type":"object","allOf":[{"type":"integer"}]}`,
		},
		{
			name:  "sentinel in a mergeable allOf child is removed after the merge",
			input: `{"type":"integer","allOf":[{"minimum":-9007199254740991,"maximum":9007199254740991}]}`,
			want:  `{"type":"integer"}`,
		},
		{
			name:  "nested allOf in properties is flattened",
			input: `{"type":"object","properties":{"p":{"allOf":[{"type":"string"},{"minLength":1}]}}}`,
			want:  `{"type":"object","properties":{"p":{"minLength":1,"type":"string"}}}`,
		},
		{
			name:  "nested allOf in array items is flattened",
			input: `{"type":"array","items":{"allOf":[{"type":"integer"},{"minimum":1}]}}`,
			want:  `{"type":"array","items":{"minimum":1,"type":"integer"}}`,
		},
		{
			name:  "allOf inside an allOf member is flattened recursively",
			input: `{"allOf":[{"allOf":[{"type":"string"},{"minLength":1}]}]}`,
			want:  `{"minLength":1,"type":"string"}`,
		},
		{
			name:  "empty allOf is removed",
			input: `{"allOf":[]}`,
			want:  `{}`,
			exact: true,
		},
		{
			name:  "non-object allOf member prevents flattening",
			input: `{"allOf":[true]}`,
			want:  `{"allOf":[true]}`,
			exact: true,
		},
		{
			name:  "UTF-8 content survives flattening",
			input: `{"properties":{"模式":{"allOf":[{"type":"string"},{"enum":["快速","慢速"]}]}}}`,
			want:  `{"properties":{"模式":{"enum":["快速","慢速"],"type":"string"}}}`,
			exact: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FlattenToolSchema(tt.input)
			if tt.exact {
				if got != tt.want {
					t.Fatalf("FlattenToolSchema() = %q, want exact %q", got, tt.want)
				}
			} else {
				assertSchemaJSONEqual(t, got, tt.want)
			}
			// Determinism: the same input must always produce the same output.
			if again := FlattenToolSchema(tt.input); again != got {
				t.Fatalf("non-deterministic result: first %q, second %q", got, again)
			}
		})
	}
}

func TestFlattenIsOptInAndComposes(t *testing.T) {
	input := `{"type":"integer","allOf":[{"minimum":0}],"properties":{"x":{"enum":["a","b"]}}}`

	// The existing projection must stay byte-for-byte in charge of its own
	// behavior: ProjectToolSchema never flattens, even with CompatAll.
	projected := ProjectToolSchema(input, CompatAll)
	assertSchemaJSONEqual(t, projected, `{"allOf":[{"minimum":0}],"properties":{"x":{"enum":["a","b"],"type":"string"}},"type":"integer"}`)

	// Callers that opt in can compose the two pure functions in either order
	// without losing enum typing or the merged allOf constraint.
	composed := ProjectToolSchema(FlattenToolSchema(input), CompatAll)
	assertSchemaJSONEqual(t, composed, `{"minimum":0,"properties":{"x":{"enum":["a","b"],"type":"string"}},"type":"integer"}`)
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
