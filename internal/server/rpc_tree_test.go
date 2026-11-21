package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// forkEvents 构造一棵带旁支的会话转录，供 session.load 的分支过滤测试使用：
// root→n1→n2→n3 是目标链，x1 从 n1 分叉出去，属于应被排除的旁支。
func forkEvents() []SessionEvent {
	return []SessionEvent{
		{ID: "root", Kind: "header"},
		{ID: "n1", Parent: "root", Kind: "user"},
		{ID: "n2", Parent: "n1", Kind: "assistant"},
		{ID: "n3", Parent: "n2", Kind: "user"},
		{ID: "x1", Parent: "n1", Kind: "user"},
	}
}

// TestRPCSessionLoadBranchFiltersChain 验证 load{id,branch} 只回传根→branchID
// 的祖先链，排除旁支，且保持原始事件顺序。
func TestRPCSessionLoadBranchFiltersChain(t *testing.T) {
	cfg := Config{Runner: &fakeRunner{}, Sessions: fakeSessions{events: forkEvents()}}
	lines := runRPCBuffer(t, cfg,
		`{"jsonrpc":"2.0","id":1,"method":"session.load","params":{"id":"s","branch":"n3"}}`)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	var resp struct {
		Result struct {
			ID     string         `json:"id"`
			Events []SessionEvent `json:"events"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Result.ID != "s" {
		t.Fatalf("id = %q, want s", resp.Result.ID)
	}
	gotIDs := make([]string, 0, len(resp.Result.Events))
	for _, ev := range resp.Result.Events {
		gotIDs = append(gotIDs, ev.ID)
	}
	want := []string{"root", "n1", "n2", "n3"}
	if len(gotIDs) != len(want) {
		t.Fatalf("event ids = %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("event ids = %v, want %v", gotIDs, want)
		}
	}
}

// TestRPCSessionLoadUnknownBranch 验证未知 branch 返回干净的 -32000 错误，
// 且分支名里夹带的绝对路径与凭据形态片段都被脱敏，不跨越 RPC 边界。
func TestRPCSessionLoadUnknownBranch(t *testing.T) {
	storage := filepath.Join(os.TempDir(), "argentgo", "sessions", "sk-secret123456789")
	cfg := Config{Runner: &fakeRunner{}, Sessions: fakeSessions{events: forkEvents()}}
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"session.load","params":{"id":"s","branch":%q}}`, storage)
	lines := runRPCBuffer(t, cfg, req)
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
	if !strings.Contains(resp.Error.Message, "unknown branch") {
		t.Fatalf("message = %q, want unknown branch", resp.Error.Message)
	}
	for _, leak := range []string{storage, "sk-secret123456789", filepath.ToSlash(storage)} {
		if strings.Contains(resp.Error.Message, leak) {
			t.Fatalf("message leaked %q: %s", leak, resp.Error.Message)
		}
	}
	if !strings.Contains(resp.Error.Message, "<storage>/...") {
		t.Fatalf("message lost path placeholder: %s", resp.Error.Message)
	}
}

// rpcFrameID 解出 JSON-RPC 帧的顶层数字 id。现有方法的请求 id 都是数字。
func rpcFrameID(frame string) (int, error) {
	var v struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal([]byte(frame), &v); err != nil {
		return 0, err
	}
	return v.ID, nil
}

// sortRPCFramesByID 按顶层 id 对帧做稳定排序。
// prompt 方法在独立 goroutine 中应答，它与后续同步方法之间的跨帧顺序没有保证；
// 排序只忽略这一维度，每帧自身的字节（键序、omitempty、错误信封）仍被逐字节锁定。
func sortRPCFramesByID(frames []string) error {
	type decorated struct {
		id    int
		frame string
	}
	items := make([]decorated, len(frames))
	for i, frame := range frames {
		id, err := rpcFrameID(frame)
		if err != nil {
			return err
		}
		items[i] = decorated{id: id, frame: frame}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].id < items[j].id })
	for i := range frames {
		frames[i] = items[i].frame
	}
	return nil
}

// TestRPCExistingMethodsWireUnchanged 锚定现有方法的逐字节响应，确保加入
// session.tree 分支没有改变既有 RPC 方法或错误信封的形态。
// 跨帧顺序因 prompt 异步应答而不确定，按 id 稳定排序后逐帧比对；
// 每帧的键序、omitempty 与错误信封仍然逐字节锁定。
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
		// 显式带空 branch 应与不分支的 session.load 逐字节一致。
		`{"jsonrpc":"2.0","id":6,"method":"session.load","params":{"id":"x","branch":""}}`,
	)
	want := []string{
		`{"id":1,"jsonrpc":"2.0","result":{"sessions":[{"id":"s1","title":"first","updated":"0001-01-01T00:00:00Z"}]}}`,
		`{"id":2,"jsonrpc":"2.0","result":{"events":[{"id":"e1","kind":"user","ts":"0001-01-01T00:00:00Z"}],"id":"x"}}`,
		`{"id":3,"jsonrpc":"2.0","result":{"ok":true}}`,
		`{"id":4,"jsonrpc":"2.0","result":{"text":"hello"}}`,
		`{"error":{"code":-32601,"message":"unknown method \"nope\""},"id":5,"jsonrpc":"2.0"}`,
		`{"id":6,"jsonrpc":"2.0","result":{"events":[{"id":"e1","kind":"user","ts":"0001-01-01T00:00:00Z"}],"id":"x"}}`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d frames, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	// prompt 走异步应答，跨帧顺序无保证；只按 id 稳定排序，之后仍逐帧逐字节比对。
	if err := sortRPCFramesByID(lines); err != nil {
		t.Fatalf("decode frame id from lines: %v", err)
	}
	if err := sortRPCFramesByID(want); err != nil {
		t.Fatalf("decode frame id from want: %v", err)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("frame %d:\n got  %s\n want %s", i, lines[i], want[i])
		}
	}
}

// TestSortRPCFramesByIDPreservesFrameBytes 证明排序只改变跨帧顺序：
// 乱序输入排序后逐字节一致；任意一帧内容真不同时，逐字节比较仍然失败。
func TestSortRPCFramesByIDPreservesFrameBytes(t *testing.T) {
	frame1 := `{"id":1,"jsonrpc":"2.0","result":{"ok":true}}`
	frame2 := `{"id":2,"jsonrpc":"2.0","result":{"ok":true}}`
	shuffled := []string{frame2, frame1}
	if err := sortRPCFramesByID(shuffled); err != nil {
		t.Fatal(err)
	}
	if shuffled[0] != frame1 || shuffled[1] != frame2 {
		t.Fatalf("sorted = %q, want [%s %s]", shuffled, frame1, frame2)
	}
	// mutant：只把一帧的内容从 ok:true 改为 ok:false，排序后仍必须与期望不同。
	mutant := []string{frame2, `{"id":1,"jsonrpc":"2.0","result":{"ok":false}}`}
	if err := sortRPCFramesByID(mutant); err != nil {
		t.Fatal(err)
	}
	if mutant[0] == frame1 {
		t.Fatal("sort masked a real per-frame content difference")
	}
}
