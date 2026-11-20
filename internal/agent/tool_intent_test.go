package agent

import (
	"encoding/json"
	"testing"
)

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
