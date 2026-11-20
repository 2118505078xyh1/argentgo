package security

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactJSONPropagatesSecretContextIntoContainers 验证以凭据命名的键下的值会被
// 一路向下脱敏。如果没有把秘密上下文穿过 map 和数组传播，像 {"tokens":["hunter2"]}
// 这样的明文秘密就会原封不动地进入审计日志：Redact 只识别具有凭据形状的字符串，
// 而任意秘密并不一定具有凭据形状。
func TestRedactJSONPropagatesSecretContextIntoContainers(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		secrets []string // 不得出现在输出的任何位置
		check   func(t *testing.T, value map[string]any)
	}{
		{
			name:    "array under secret key",
			input:   `{"tokens":["hunter2","letmein"],"note":"ok"}`,
			secrets: []string{"hunter2", "letmein"},
			check: func(t *testing.T, value map[string]any) {
				list, ok := value["tokens"].([]any)
				if !ok || len(list) != 2 {
					t.Fatalf("array shape changed: %v", value["tokens"])
				}
				for i, item := range list {
					if item != "[redacted:token]" {
						t.Fatalf("tokens[%d] = %v, want token placeholder", i, item)
					}
				}
				if value["note"] != "ok" {
					t.Fatalf("non-secret key changed: %v", value["note"])
				}
			},
		},
		{
			name:    "nested array under secret key",
			input:   `{"env":{"passwords":["a","b"]}}`,
			secrets: []string{"\"a\"", "\"b\""},
			check: func(t *testing.T, value map[string]any) {
				env := value["env"].(map[string]any)
				list := env["passwords"].([]any)
				if len(list) != 2 || list[0] != "[redacted:password]" || list[1] != "[redacted:password]" {
					t.Fatalf("nested passwords not redacted: %v", list)
				}
			},
		},
		{
			name:    "map under secret key",
			input:   `{"secret":{"nested":"hunter2"}}`,
			secrets: []string{"hunter2"},
			check: func(t *testing.T, value map[string]any) {
				inner := value["secret"].(map[string]any)
				if inner["nested"] != "[redacted:secret]" {
					t.Fatalf("string nested under secret key not redacted: %v", inner)
				}
			},
		},
		{
			name:    "specific nested key keeps its own kind",
			input:   `{"api_key":{"value":"hunter2","password":"letmein"}}`,
			secrets: []string{"hunter2", "letmein"},
			check: func(t *testing.T, value map[string]any) {
				inner := value["api_key"].(map[string]any)
				if inner["value"] != "[redacted:api key]" {
					t.Fatalf("inherited kind wrong: %v", inner["value"])
				}
				if inner["password"] != "[redacted:password]" {
					t.Fatalf("specific key kind should win: %v", inner["password"])
				}
			},
		},
		{
			name:  "ordinary composite keeps structure and values",
			input: `{"items":[{"tool":"read","password":"hunter2"}],"values":[1,2]}`,
			check: func(t *testing.T, value map[string]any) {
				items := value["items"].([]any)
				item := items[0].(map[string]any)
				if item["tool"] != "read" || item["password"] != "[redacted:password]" {
					t.Fatalf("ordinary array entry changed: %v", item)
				}
				values := value["values"].([]any)
				if len(values) != 2 || values[0] != float64(1) || values[1] != float64(2) {
					t.Fatalf("non-secret array changed: %v", values)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactJSON([]byte(tc.input))
			for _, secret := range tc.secrets {
				if strings.Contains(out, secret) {
					t.Fatalf("secret %q leaked from RedactJSON: %q", secret, out)
				}
			}
			var value map[string]any
			if err := json.Unmarshal([]byte(out), &value); err != nil {
				t.Fatalf("redacted output is not JSON: %v (%q)", err, out)
			}
			tc.check(t, value)
		})
	}
}
