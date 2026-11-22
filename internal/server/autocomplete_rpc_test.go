package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// fakeCompletionProvider 是测试用的补全提供者：记住 Learn 的文本，
// Autocomplete 返回一条确定性建议，便于断言 wire 结构。
type fakeCompletionProvider struct {
	learned string
	prefix  string
	limit   int
}

func (f *fakeCompletionProvider) Learn(text string) { f.learned = text }

func (f *fakeCompletionProvider) Autocomplete(prefix string, limit int) []CompletionSuggestion {
	f.prefix, f.limit = prefix, limit
	return []CompletionSuggestion{{Token: prefix + "X", Score: 0.75}}
}

// TestRPCAutocompleteNotConfigured 验证未注入 provider 时返回干净错误，
// 不影响其它 RPC 方法（错误码 -32000）。
func TestRPCAutocompleteNotConfigured(t *testing.T) {
	lines := runRPCBuffer(t, Config{}, `{"jsonrpc":"2.0","id":1,"method":"spelling.autocomplete","params":{"prefix":"ab"}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 frame, got %d: %v", len(lines), lines)
	}
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != codeServerError {
		t.Fatalf("error code = %d, want %d: %s", resp.Error.Code, codeServerError, lines[0])
	}
}

// TestRPCAutocompleteLearnAndComplete 验证 learn → autocomplete 闭环：
// provider 收到训练文本与前缀，响应含 suggestions 且分数原样透出。
func TestRPCAutocompleteLearnAndComplete(t *testing.T) {
	provider := &fakeCompletionProvider{}
	cfg := Config{CompletionProvider: provider}
	lines := runRPCBuffer(t, cfg,
		`{"jsonrpc":"2.0","id":1,"method":"spelling.autocomplete","params":{"prefix":"fmt.","limit":7,"learn":"fmt.Println()"}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 frame, got %d: %v", len(lines), lines)
	}
	if provider.learned != "fmt.Println()" {
		t.Fatalf("learn not delivered: %q", provider.learned)
	}
	if provider.prefix != "fmt." || provider.limit != 7 {
		t.Fatalf("prefix/limit = %q/%d, want fmt./7", provider.prefix, provider.limit)
	}
	if !strings.Contains(lines[0], `"token":"fmt.X"`) || !strings.Contains(lines[0], `"score":0.75`) {
		t.Fatalf("suggestion wire missing/incorrect: %s", lines[0])
	}
}

// TestRPCAutocompleteLimitBounds 验证 limit 的默认值与上限钳制。
func TestRPCAutocompleteLimitBounds(t *testing.T) {
	provider := &fakeCompletionProvider{}
	cfg := Config{CompletionProvider: provider}
	runRPCBuffer(t, cfg, `{"jsonrpc":"2.0","id":1,"method":"spelling.autocomplete","params":{"prefix":"a"}}`)
	if provider.limit != 5 {
		t.Fatalf("default limit = %d, want 5", provider.limit)
	}
	runRPCBuffer(t, cfg, `{"jsonrpc":"2.0","id":2,"method":"spelling.autocomplete","params":{"prefix":"a","limit":999}}`)
	if provider.limit != 32 {
		t.Fatalf("clamped limit = %d, want 32", provider.limit)
	}
}
