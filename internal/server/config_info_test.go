package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeConfigInfo 是注入 Config.ConfigInfoProvider 的假来源。
type fakeConfigInfo struct {
	info ConfigInfo
}

func (f fakeConfigInfo) ConfigInfo() ConfigInfo { return f.info }

// TestRPCConfigInfoNotConfigured 验证未注入提供者时返回干净的 -32000，
// 而不是 panic 或把错误伪装成成功（与 session.tree 的零值语义一致）。
func TestRPCConfigInfoNotConfigured(t *testing.T) {
	lines := runRPCBuffer(t, Config{Runner: &fakeRunner{}},
		`{"jsonrpc":"2.0","id":1,"method":"config.info"}`)
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

// TestRPCConfigInfoExactWire 验证注入后返回精确的 wire 形态：字段顺序
// model/provider/workspace 由结构体标签固定，且普通值不被脱敏规则改动。
func TestRPCConfigInfoExactWire(t *testing.T) {
	lines := runRPCBuffer(t, Config{
		Runner:             &fakeRunner{},
		ConfigInfoProvider: fakeConfigInfo{info: ConfigInfo{Model: "gpt-4o", Provider: "openai", Workspace: "my-workspace"}},
	}, `{"jsonrpc":"2.0","id":1,"method":"config.info"}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	want := `{"id":1,"jsonrpc":"2.0","result":{"model":"gpt-4o","provider":"openai","workspace":"my-workspace"}}`
	if lines[0] != want {
		t.Fatalf("wire:\n got  %s\n want %s", lines[0], want)
	}
}

// TestRPCConfigInfoScrubsAbsolutePaths 验证 workspace 的绝对路径与
// model 中混入的凭据都不得跨越 RPC 边界；两者复用 session.load 的脱敏边界。
func TestRPCConfigInfoScrubsAbsolutePaths(t *testing.T) {
	abs := filepath.Join(os.TempDir(), "argentgo", "workspace", "demo")
	secret := "sk-testabcdef0123456789"
	cfg := Config{
		Runner: &fakeRunner{},
		ConfigInfoProvider: fakeConfigInfo{info: ConfigInfo{
			Model:     "gpt-4o-" + secret,
			Provider:  "openai",
			Workspace: abs,
		}},
	}
	lines := runRPCBuffer(t, cfg, `{"jsonrpc":"2.0","id":7,"method":"config.info"}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	got := decodeConfigInfoResult(t, lines[0])
	for _, leak := range []string{abs, filepath.ToSlash(abs), secret} {
		if strings.Contains(got.Workspace, leak) || strings.Contains(got.Model, leak) {
			t.Fatalf("result leaked %q: %+v", leak, got)
		}
	}
	if !strings.Contains(got.Workspace, "<storage>/...") {
		t.Fatalf("workspace lost path placeholder: %+v", got)
	}
	// Windows 盘符根即便在没有 os.TempDir 绝对路径的机器上也应被折叠。
	lines = runRPCBuffer(t, Config{
		Runner:             &fakeRunner{},
		ConfigInfoProvider: fakeConfigInfo{info: ConfigInfo{Workspace: `C:\Users\tester\project`}},
	}, `{"jsonrpc":"2.0","id":8,"method":"config.info"}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	got = decodeConfigInfoResult(t, lines[0])
	if strings.Contains(got.Workspace, `C:\Users`) || strings.Contains(got.Workspace, "C:/Users") {
		t.Fatalf("windows path leaked: %+v", got)
	}
	if !strings.Contains(got.Workspace, "<storage>/...") {
		t.Fatalf("windows path not folded: %+v", got)
	}
}

// decodeConfigInfoResult 把一条 config.info 结果帧解码为 ConfigInfo，
// 使断言作用在 JSON 解码后的字符串上（wire 会把 < > 转义为 \u003c \u003e）。
func decodeConfigInfoResult(t *testing.T, line string) ConfigInfo {
	t.Helper()
	var resp struct {
		Result ConfigInfo `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Result
}

// TestRPCConfigInfoCaseKeepsExistingMethodsByteIdentical 锚定新增分支后既有
// RPC 方法的逐字节响应不变；config.info 只多出一条结果帧，不扰动其他信封。
func TestRPCConfigInfoCaseKeepsExistingMethodsByteIdentical(t *testing.T) {
	sessions := fakeSessions{
		infos:  []SessionInfo{{ID: "s1", Title: "first"}},
		events: []SessionEvent{{ID: "e1", Kind: "user"}},
	}
	cfg := Config{
		Runner:             &fakeRunner{reply: "hello"},
		Sessions:           sessions,
		ConfigInfoProvider: fakeConfigInfo{info: ConfigInfo{Model: "m", Provider: "p", Workspace: "w"}},
	}
	// 只放同步方法：prompt 由异步 goroutine 响应，顺序不稳定，其字节锚点
	// 已由 rpc_tree_test.go 的 TestRPCExistingMethodsWireUnchanged 覆盖。
	lines := runRPCBuffer(t, cfg,
		`{"jsonrpc":"2.0","id":1,"method":"session.list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"session.load","params":{"id":"x"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"abort"}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
		`{"jsonrpc":"2.0","id":6,"method":"config.info"}`,
	)
	want := []string{
		`{"id":1,"jsonrpc":"2.0","result":{"sessions":[{"id":"s1","title":"first","updated":"0001-01-01T00:00:00Z"}]}}`,
		`{"id":2,"jsonrpc":"2.0","result":{"events":[{"id":"e1","kind":"user","ts":"0001-01-01T00:00:00Z"}],"id":"x"}}`,
		`{"id":3,"jsonrpc":"2.0","result":{"ok":true}}`,
		`{"error":{"code":-32601,"message":"unknown method \"nope\""},"id":5,"jsonrpc":"2.0"}`,
		`{"id":6,"jsonrpc":"2.0","result":{"model":"m","provider":"p","workspace":"w"}}`,
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
