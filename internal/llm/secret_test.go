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

func TestRedactTextMasksMainstreamAPIKeyFormats(t *testing.T) {
	const (
		anthropicKey = "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
		projectKey   = "sk-proj-abcDEFghIJ1234567890XYZabcdefghij"
		plainKey     = "sk-abcdefghijklmnopqrstuvwxyz"
		revokeKey    = "rk-braintree-abcdefghijklmnopqrstuv"
		publishKey   = "pk_live_abcdefghijklmnopqrstuvwxyz"
		genericKey   = "mySecretTokenValue1234567890"
		bearerToken  = "sk-proj-abcDEFghIJ1234567890XYZabcdefghij"
	)

	cases := []struct {
		name     string
		in       string
		mustHide []string
		mustKeep []string
	}{
		{
			name:     "Anthropic sk-ant-api03 hyphenated key",
			in:       fmt.Sprintf("Invalid API key: %s", anthropicKey),
			mustHide: []string{anthropicKey, "api03-ABCDEFGHIJKLMNOP"},
			mustKeep: []string{"Invalid API key:"},
		},
		{
			name:     "OpenAI sk-proj key after Bearer scheme",
			in:       "Authorization: Bearer " + bearerToken,
			mustHide: []string{bearerToken, "Bearer"},
			mustKeep: []string{"Authorization:"},
		},
		{
			name:     "plain sk key in prose",
			in:       "bad key " + plainKey + " for alice@example.com",
			mustHide: []string{plainKey, "alice@example.com"},
			mustKeep: []string{"bad key", "for"},
		},
		{
			name:     "rk and pk with either separator",
			in:       fmt.Sprintf("rk: %s pk: %s", revokeKey, publishKey),
			mustHide: []string{revokeKey, publishKey},
			mustKeep: []string{"rk:", "pk:"},
		},
		{
			name:     "API key with a space between words",
			in:       fmt.Sprintf("API key: %s", genericKey),
			mustHide: []string{genericKey},
			mustKeep: []string{"API key:"},
		},
		{
			name:     "api-key and api_key field names",
			in:       fmt.Sprintf("api-key=%s, api_key: %s", genericKey, plainKey),
			mustHide: []string{genericKey, plainKey},
			mustKeep: []string{"api-key=", "api_key:"},
		},
		{
			name:     "mixed Chinese and English error",
			in:       fmt.Sprintf("密钥 %s 无效，API key: %s", anthropicKey, projectKey),
			mustHide: []string{anthropicKey, projectKey, "api03-ABCDEFGHIJKLMNOP"},
			mustKeep: []string{"密钥", "无效", "API key:"},
		},
		{
			name:     "JSON authorization with quoted scheme and token",
			in:       fmt.Sprintf(`{"authorization": "Bearer %s", "model": "m"}`, projectKey),
			mustHide: []string{projectKey, "Bearer"},
			mustKeep: []string{"authorization", "model", "m"},
		},
		{
			name:     "unknown auth scheme still swallows the token",
			in:       "Authorization: Mac " + genericKey,
			mustHide: []string{genericKey},
			mustKeep: []string{"Authorization:"},
		},
		{
			name:     "ordinary error wording is untouched",
			in:       "provider returned 400: model not found",
			mustKeep: []string{"provider returned 400: model not found"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.RedactText(tc.in)
			for _, hidden := range tc.mustHide {
				if strings.Contains(got, hidden) {
					t.Errorf("RedactText leaked %q: %q", hidden, got)
				}
			}
			for _, kept := range tc.mustKeep {
				if !strings.Contains(got, kept) {
					t.Errorf("RedactText lost %q: %q", kept, got)
				}
			}
		})
	}
}

func TestRedactErrorMasksBearerKey(t *testing.T) {
	const token = "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	err := fmt.Errorf("request failed: Authorization: Bearer %s", token)
	got := llm.RedactError(err)
	if strings.Contains(got, token) {
		t.Errorf("RedactError leaked the key: %q", got)
	}
	if !strings.Contains(got, "request failed:") || !strings.Contains(got, "Authorization:") {
		t.Errorf("RedactError lost error wording: %q", got)
	}
	if got := llm.RedactError(nil); got != "" {
		t.Errorf("RedactError(nil) = %q, want empty", got)
	}
}
