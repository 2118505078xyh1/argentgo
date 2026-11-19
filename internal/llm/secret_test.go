package llm_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const testSecretValue = "sk_secretvalue1234567890"

func TestSecretNeverLeaksThroughFormattingOrJSON(t *testing.T) {
	secret := llm.NewSecret(testSecretValue)
	if secret.Value() != testSecretValue {
		t.Fatalf("Value() = %q", secret.Value())
	}

	forms := map[string]string{
		"String":        secret.String(),
		"GoString":      secret.GoString(),
		"Sprint":        fmt.Sprint(secret),
		"v":             fmt.Sprintf("%v", secret),
		"plus-v":        fmt.Sprintf("%+v", secret),
		"sharp-v":       fmt.Sprintf("%#v", secret),
		"nested struct": fmt.Sprintf("%+v", struct{ Key llm.Secret }{secret}),
		"nested slice":  fmt.Sprint([]llm.Secret{secret}),
		"nested map":    fmt.Sprintf("%v", map[string]llm.Secret{"key": secret}),
		"pointer":       fmt.Sprintf("%+v", &secret),
	}
	for name, got := range forms {
		if strings.Contains(got, testSecretValue) {
			t.Errorf("%s leaked the secret: %q", name, got)
		}
		if !strings.Contains(got, llm.Redacted) {
			t.Errorf("%s did not use the placeholder: %q", name, got)
		}
	}

	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("Marshal Secret: %v", err)
	}
	if strings.Contains(string(encoded), testSecretValue) {
		t.Errorf("JSON leaked the secret: %s", encoded)
	}
	if string(encoded) != `"`+llm.Redacted+`"` {
		t.Errorf("JSON = %s, want quoted placeholder", encoded)
	}

	encoded, err = json.Marshal(map[string]any{"apiKey": secret, "nested": []llm.Secret{secret}})
	if err != nil {
		t.Fatalf("Marshal nested Secret: %v", err)
	}
	if strings.Contains(string(encoded), testSecretValue) {
		t.Errorf("nested JSON leaked the secret: %s", encoded)
	}
}

func TestRedactHeadersHidesCredentialsAndKeepsRequestHeaders(t *testing.T) {
	headers := http.Header{
		"Authorization":   {"Bearer " + testSecretValue},
		"X-Api-Key":       {testSecretValue},
		"Cookie":          {"session=" + testSecretValue},
		"Content-Type":    {"application/json"},
		"X-Goog-Api-Key":  {testSecretValue, "second"},
		"X-Custom-Header": {"keep me"},
	}

	redacted := llm.RedactHeaders(headers)
	for _, name := range []string{"Authorization", "X-Api-Key", "Cookie", "X-Goog-Api-Key"} {
		for _, value := range redacted.Values(name) {
			if value != llm.Redacted {
				t.Errorf("%s = %q, want placeholder", name, value)
			}
		}
	}
	if got := redacted.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := redacted.Get("X-Custom-Header"); got != "keep me" {
		t.Errorf("X-Custom-Header = %q", got)
	}
	if got := headers.Get("Authorization"); got != "Bearer "+testSecretValue {
		t.Errorf("RedactHeaders mutated the input: %q", got)
	}
	if llm.RedactHeaders(nil) != nil {
		t.Fatal("RedactHeaders(nil) should return nil")
	}
}

func TestRedactURLStripsQueryAndUserinfo(t *testing.T) {
	raw := "https://alice:" + testSecretValue + "@api.example.com/v1/messages?api_key=" + testSecretValue + "&model=m"
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	redacted := llm.RedactURL(parsed)
	out := redacted.String()
	for _, hidden := range []string{testSecretValue, "alice", "api_key"} {
		if strings.Contains(out, hidden) {
			t.Errorf("RedactURL leaked %q: %s", hidden, out)
		}
	}
	if redacted.Host != "api.example.com" || redacted.Path != "/v1/messages" {
		t.Errorf("RedactURL lost host/path: %s", out)
	}
	if redacted.Query().Get("model") != "" {
		t.Errorf("query was not stripped: %s", out)
	}
	if parsed.User == nil || parsed.RawQuery == "" {
		t.Fatal("RedactURL mutated the input")
	}
	if llm.RedactURL(nil) != nil {
		t.Fatal("RedactURL(nil) should return nil")
	}
}

func TestRedactJSONFieldsMasksDefaultsAndCustomFields(t *testing.T) {
	input := map[string]any{
		"api_key":  testSecretValue,
		"-model":   "keep",
		"nested":   map[string]any{"access_token": testSecretValue, "visible": 7},
		"list":     []any{map[string]any{"client_secret": testSecretValue}, "plain"},
		"my-token": testSecretValue,
	}
	got := llm.RedactJSONFields(input, []string{"my-token"})
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), testSecretValue) {
		t.Fatalf("RedactJSONFields leaked the secret: %s", encoded)
	}
	if !strings.Contains(string(encoded), llm.Redacted) {
		t.Fatalf("no placeholder in %s", encoded)
	}
	if got["nested"].(map[string]any)["visible"] != 7 || got["-model"] != "keep" {
		t.Fatalf("ordinary fields were lost: %#v", got)
	}
	if encodedInput, _ := json.Marshal(input); strings.Contains(string(encodedInput), llm.Redacted) {
		t.Fatal("RedactJSONFields mutated the input")
	}
	if llm.RedactJSONFields(nil, nil) != nil {
		t.Fatal("RedactJSONFields(nil) should return nil")
	}
}
