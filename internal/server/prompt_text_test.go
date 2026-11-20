package server

import "testing"

// TestPromptText 验证 HTTP、--rpc 和 acp 共用的单一提示提取实现。
// 每个用例都分别通过 promptText（RPC/ACP 参数）
// 和 requestText（HTTP 体）运行，以证明两个入口保持统一。
func TestPromptText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"raw text", "hello world", "hello world"},
		{"raw text is trimmed", "  hello world \n", "hello world"},
		{"json text field", `{"text":"hello"}`, "hello"},
		{"single string prompt", `{"prompt":"hello"}`, "hello"},
		{"prompt blocks", `{"prompt":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}`, "hello\nworld"},
		{"blocks skip non-text", `{"prompt":[{"type":"image","text":"ignored"},{"type":"text","text":"kept"}]}`, "kept"},
		{"blocks allow empty type", `{"prompt":[{"text":"kept"}]}`, "kept"},
		{"text wins over prompt", `{"text":"first","prompt":"second"}`, "first"},
		{"blank text falls through to prompt", `{"text":"   ","prompt":"fallback"}`, "fallback"},
		{"json text keeps original spacing", `{"text":"  hello  "}`, "  hello  "},
		{"null prompt", `{"prompt":null}`, ""},
		{"empty prompt string", `{"prompt":""}`, ""},
		{"empty object", `{}`, ""},
		{"whitespace only", "  \n\t  ", ""},
		{"empty input", "", ""},
		{"invalid json object", `{"prompt":`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptText([]byte(tc.raw)); got != tc.want {
				t.Fatalf("promptText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if got := requestText([]byte(tc.raw)); got != tc.want {
				t.Fatalf("requestText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
