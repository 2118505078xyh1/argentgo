package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRPCStatusNotConfigured 验证未注入 StatusProvider 时 status 返回干净的
// -32000，而不是 panic 或伪装成成功（与 config.info 的零值语义一致）。
func TestRPCStatusNotConfigured(t *testing.T) {
	lines := runRPCBuffer(t, Config{Runner: &fakeRunner{}},
		`{"jsonrpc":"2.0","id":1,"method":"status"}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("response = %s, want -32000", lines[0])
	}
	if !strings.Contains(resp.Error.Message, "not configured") {
		t.Fatalf("message = %q, want not configured", resp.Error.Message)
	}
}

// TestRPCStatusExactWire 验证注入后 status 把状态 JSON 作为 result 对象
// 直接嵌入（而非字符串），字段顺序与既有信封一致，且 CJK 不被转义为 \uXXXX。
func TestRPCStatusExactWire(t *testing.T) {
	body := []byte(`{"turn":{"values":{"阶段":"流式输出 中文"}}}`)
	lines := runRPCBuffer(t, Config{
		Runner:         &fakeRunner{},
		StatusProvider: staticStatusProvider(body),
	}, `{"jsonrpc":"2.0","id":9,"method":"status"}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	want := `{"id":9,"jsonrpc":"2.0","result":{"turn":{"values":{"阶段":"流式输出 中文"}}}}`
	if lines[0] != want {
		t.Fatalf("wire:\n got  %s\n want %s", lines[0], want)
	}
}

// TestRPCStatusRawMessageRejectsInvalidProviderBytes 验证提供者返回非 JSON
// 字节时，整条响应仍是合法 JSON-RPC 帧（json.Marshal 拒绝非法 RawMessage
// 时不写出半帧），不会污染传输。
func TestRPCStatusRawMessageRejectsInvalidProviderBytes(t *testing.T) {
	lines := runRPCBuffer(t, Config{
		Runner:         &fakeRunner{},
		StatusProvider: staticStatusProvider([]byte(`not-json`)),
	}, `{"jsonrpc":"2.0","id":3,"method":"status"}`)
	// 非法 RawMessage 使 p.write 静默失败：不产生任何帧，传输继续。
	if len(lines) != 0 {
		t.Fatalf("got %d frames (%q), want 0", len(lines), lines)
	}
}

// TestRPCStatusCaseKeepsExistingMethodsByteIdentical 锚定新增 status 分支后
// 既有 RPC 方法的逐字节响应不变：status 只多出一条结果帧，不扰动其它信封。
func TestRPCStatusCaseKeepsExistingMethodsByteIdentical(t *testing.T) {
	sessions := fakeSessions{
		infos:  []SessionInfo{{ID: "s1", Title: "first"}},
		events: []SessionEvent{{ID: "e1", Kind: "user"}},
	}
	cfg := Config{
		Runner:         &fakeRunner{reply: "hello"},
		Sessions:       sessions,
		StatusProvider: staticStatusProvider([]byte(`{"integrity":{"values":{"model":"m"}}}`)),
	}
	// 只放同步方法：prompt 由异步 goroutine 响应，顺序不稳定。
	lines := runRPCBuffer(t, cfg,
		`{"jsonrpc":"2.0","id":1,"method":"session.list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"session.load","params":{"id":"x"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"abort"}`,
		`{"jsonrpc":"2.0","id":4,"method":"status"}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	)
	want := []string{
		`{"id":1,"jsonrpc":"2.0","result":{"sessions":[{"id":"s1","title":"first","updated":"0001-01-01T00:00:00Z"}]}}`,
		`{"id":2,"jsonrpc":"2.0","result":{"events":[{"id":"e1","kind":"user","ts":"0001-01-01T00:00:00Z"}],"id":"x"}}`,
		`{"id":3,"jsonrpc":"2.0","result":{"ok":true}}`,
		`{"id":4,"jsonrpc":"2.0","result":{"integrity":{"values":{"model":"m"}}}}`,
		`{"error":{"code":-32601,"message":"unknown method \"nope\""},"id":5,"jsonrpc":"2.0"}`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d frames, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("frame %d:\n got  %s\n want %s", i, lines[i], want[i])
		}
	}
}
