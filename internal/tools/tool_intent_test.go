package tools

import (
	"encoding/json"
	"testing"
)

func TestInjectIntentSchemaAddsFirstPropertyAndRequired(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"],"additionalProperties":false}`)
	want := json.RawMessage(`{"type":"object","properties":{"i":{"type":"string","description":"concise intent for this call"},"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path","i"],"additionalProperties":false}`)

	got := InjectIntentSchema(in)
	if string(got) != string(want) {
		t.Fatalf("InjectIntentSchema() = %s\nwant %s", got, want)
	}
}

func TestInjectIntentSchemaCreatesRequiredWhenAbsent(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	want := json.RawMessage(`{"type":"object","properties":{"i":{"type":"string","description":"concise intent for this call"},"path":{"type":"string"}},"required":["i"]}`)

	got := InjectIntentSchema(in)
	if string(got) != string(want) {
		t.Fatalf("InjectIntentSchema() = %s\nwant %s", got, want)
	}
}

func TestInjectIntentSchemaIsIdempotent(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	once := InjectIntentSchema(in)
	twice := InjectIntentSchema(once)
	if string(twice) != string(once) {
		t.Fatalf("second injection changed schema:\n got %s\nwant %s", twice, once)
	}
}

func TestInjectIntentSchemaLeavesNonObjectsAndInvalidUnchanged(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"array", `["a","b"]`},
		{"string", `"hello"`},
		{"number", `42`},
		{"null", `null`},
		{"bool", `true`},
		{"empty", ``},
		{"trailing garbage", `{"type":"object"} extra`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := json.RawMessage(tc.in)
			if got := InjectIntentSchema(in); string(got) != tc.in {
				t.Fatalf("InjectIntentSchema(%q) = %q, want unchanged", tc.in, got)
			}
		})
	}
}

func TestInjectIntentSchemaPreservesUnicodeInOtherFields(t *testing.T) {
	in := json.RawMessage(`{"properties":{"desc":{"type":"string","description":"中文 😀 \"quoted\""}}}`)
	got := InjectIntentSchema(in)
	var parsed struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if v := parsed.Properties["desc"].Description; v != `中文 😀 "quoted"` {
		t.Fatalf("description changed: got %q", v)
	}
	if _, ok := parsed.Properties["i"]; !ok {
		t.Fatal("i property missing")
	}
}

func TestExtractIntentRemovesIntentAndKeepsOtherFields(t *testing.T) {
	in := json.RawMessage(`{"i":"read the config","path":"a.go","limit":5}`)
	intent, cleaned := ExtractIntent(in)
	if intent != "read the config" {
		t.Fatalf("intent = %q, want %q", intent, "read the config")
	}
	if want := `{"path":"a.go","limit":5}`; string(cleaned) != want {
		t.Fatalf("cleaned = %s, want %s", cleaned, want)
	}
}

func TestExtractIntentWithoutIntentReturnsOriginal(t *testing.T) {
	in := json.RawMessage("  {\"path\":\"a.go\"}  ")
	intent, cleaned := ExtractIntent(in)
	if intent != "" {
		t.Fatalf("intent = %q, want empty", intent)
	}
	if string(cleaned) != string(in) {
		t.Fatalf("cleaned = %q, want original bytes %q", cleaned, in)
	}
}

func TestExtractIntentInvalidOrNonObjectReturnsOriginal(t *testing.T) {
	for _, in := range []string{`["a"]`, `"str"`, `123`, `null`, `not json`} {
		raw := json.RawMessage(in)
		intent, cleaned := ExtractIntent(raw)
		if intent != "" || string(cleaned) != in {
			t.Fatalf("ExtractIntent(%q) = (%q, %q), want (\"\", %q)", in, intent, cleaned, in)
		}
	}
}

func TestExtractIntentRoundTripsQuotesAndUnicode(t *testing.T) {
	wantIntent := `say "hi" — 中文 😀`
	in, err := json.Marshal(map[string]any{
		"i":    wantIntent,
		"path": "x.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, cleaned := ExtractIntent(in)
	if intent != wantIntent {
		t.Fatalf("intent = %q, want %q", intent, wantIntent)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(cleaned, &args); err != nil {
		t.Fatalf("cleaned is not valid JSON: %v", err)
	}
	if _, ok := args["i"]; ok {
		t.Fatal("cleaned args still contain i")
	}
	if got := string(args["path"]); got != `"x.go"` {
		t.Fatalf("cleaned path = %s, want %q", got, `"x.go"`)
	}
}

func TestExtractIntentNonStringIntentIsStripped(t *testing.T) {
	in := json.RawMessage(`{"i":42,"path":"a.go"}`)
	intent, cleaned := ExtractIntent(in)
	if intent != "" {
		t.Fatalf("intent = %q, want empty for non-string i", intent)
	}
	if want := `{"path":"a.go"}`; string(cleaned) != want {
		t.Fatalf("cleaned = %s, want %s", cleaned, want)
	}
}
