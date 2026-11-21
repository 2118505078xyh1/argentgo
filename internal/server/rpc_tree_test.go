package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// treeSessions 是同时实现 SessionStore 与可选 SessionTreeStore 的假存储。
type treeSessions struct {
	branches []SessionBranch
	err      error
}

func (f treeSessions) List() ([]SessionInfo, error)             { return nil, nil }
func (f treeSessions) Load(string) ([]SessionEvent, error)      { return nil, nil }
func (f treeSessions) Branches(string) ([]SessionBranch, error) { return f.branches, f.err }

// runRPCBuffer 在内存缓冲区上跑一轮 RPC，并把第 n 行输出作为原始字节返回，
// 使断言可以比较逐字节 wire 形态。
func runRPCBuffer(t *testing.T, cfg Config, requests ...string) []string {
	t.Helper()
	var in, out bytes.Buffer
	for _, req := range requests {
		in.WriteString(req)
		in.WriteByte('\n')
	}
	if err := RunRPC(context.Background(), &in, &out, cfg); err != nil {
		t.Fatalf("RunRPC: %v", err)
	}
	raw := strings.TrimRight(out.String(), "\n")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// TestRPCSessionTreeReturnsBranches 验证 session.tree 返回分支 wire 结构，
// 且键顺序（id/parent/kind/depth/active/label/fork）与 omitempty 行为稳定。
func TestRPCSessionTreeReturnsBranches(t *testing.T) {
	branches := []SessionBranch{
		{ID: "root", Kind: "header", Depth: 0, Label: "Root", Fork: true},
		{ID: "a", Parent: "root", Kind: "user", Depth: 1, Active: true},
		{ID: "b", Parent: "root", Kind: "user", Depth: 1, Fork: true},
	}
	store := treeSessions{branches: branches}
	lines := runRPCBuffer(t, Config{Runner: &fakeRunner{}, Sessions: store},
		`{"jsonrpc":"2.0","id":1,"method":"session.tree","params":{"id":"s1"}}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	var resp struct {
		Result struct {
			Branches []SessionBranch `json:"branches"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got := resp.Result.Branches
	if len(got) != 3 {
		t.Fatalf("branches = %+v, want 3", got)
	}
	if got[0].ID != "root" || got[0].Depth != 0 || !got[0].Fork || got[0].Label != "Root" {
		t.Fatalf("branch[0] = %+v", got[0])
	}
	if got[1].Parent != "root" || got[1].Depth != 1 || !got[1].Active {
		t.Fatalf("branch[1] = %+v", got[1])
	}
	if got[2].Fork != true || got[2].Active {
		t.Fatalf("branch[2] = %+v", got[2])
	}
	// 键顺序与 omitempty 由 json.Marshal 对结构体的编码决定；
	// 期望字节必须原样出现在 wire 上。
	want, _ := json.Marshal(branches)
	if !strings.Contains(lines[0], string(want)) {
		t.Fatalf("wire = %s\nwant branches bytes = %s", lines[0], want)
	}
}

// TestRPCSessionTreeWithoutStore 验证未实现可选接口时返回干净的 -32000。
func TestRPCSessionTreeWithoutStore(t *testing.T) {
	// fakeSessions 只实现 List/Load，不带 Branches。
	lines := runRPCBuffer(t, Config{Runner: &fakeRunner{}, Sessions: fakeSessions{}},
		`{"jsonrpc":"2.0","id":1,"method":"session.tree","params":{"id":"s1"}}`)
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

// TestRPCSessionTreeRequiresID 验证缺少 id 时返回 -32602，即使 store 已配置。
func TestRPCSessionTreeRequiresID(t *testing.T) {
	lines := runRPCBuffer(t, Config{Runner: &fakeRunner{}, Sessions: treeSessions{}},
		`{"jsonrpc":"2.0","id":1,"method":"session.tree","params":{}}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	var resp struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("response = %s, want -32602", lines[0])
	}
}

// TestRPCSessionTreeErrorIsScrubbed 验证 Branches 的错误不会把绝对存储
// 路径或凭据泄漏到 wire 上，与 session.load 使用同一脱敏边界。
func TestRPCSessionTreeErrorIsScrubbed(t *testing.T) {
	storage := filepath.Join(os.TempDir(), "argentgo", "sessions", "broken.jsonl")
	store := treeSessions{err: fmt.Errorf("session: read %s: corrupt transcript with sk-secret123456789", storage)}
	lines := runRPCBuffer(t, Config{Runner: &fakeRunner{}, Sessions: store},
		`{"jsonrpc":"2.0","id":7,"method":"session.tree","params":{"id":"broken"}}`)
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
	for _, leak := range []string{storage, "sk-secret123456789", filepath.ToSlash(storage)} {
		if strings.Contains(resp.Error.Message, leak) {
			t.Fatalf("message leaked %q: %s", leak, resp.Error.Message)
		}
	}
	if !strings.Contains(resp.Error.Message, "<storage>/...") || !strings.Contains(resp.Error.Message, "corrupt transcript") {
		t.Fatalf("message lost reason or placeholder: %s", resp.Error.Message)
	}
}

// TestRPCExistingMethodsWireUnchanged 锚定现有方法的逐字节响应，确保加入
// session.tree 分支没有改变既有 RPC 方法或错误信封的形态。
func TestRPCExistingMethodsWireUnchanged(t *testing.T) {
	sessions := fakeSessions{
		infos:  []SessionInfo{{ID: "s1", Title: "first"}},
		events: []SessionEvent{{ID: "e1", Kind: "user"}},
	}
	cfg := Config{Runner: &fakeRunner{reply: "hello"}, Sessions: sessions}
	lines := runRPCBuffer(t, cfg,
		`{"jsonrpc":"2.0","id":1,"method":"session.list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"session.load","params":{"id":"x"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"abort"}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompt","params":{"text":"hi"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	)
	want := []string{
		`{"id":1,"jsonrpc":"2.0","result":{"sessions":[{"id":"s1","title":"first","updated":"0001-01-01T00:00:00Z"}]}}`,
		`{"id":2,"jsonrpc":"2.0","result":{"events":[{"id":"e1","kind":"user","ts":"0001-01-01T00:00:00Z"}],"id":"x"}}`,
		`{"id":3,"jsonrpc":"2.0","result":{"ok":true}}`,
		`{"id":4,"jsonrpc":"2.0","result":{"text":"hello"}}`,
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
