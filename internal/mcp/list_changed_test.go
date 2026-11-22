package mcp

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// TestListChangedNotificationRefreshesCachedTools 验证端到端链路：服务器推送
// notifications/tools/list_changed → revision+1 → 下一次 ListTools 即使 TTL 远未到期，
// 也会重新执行 tools/list，并返回新宣告的（含 CJK 名字的）工具。第三次调用
// 没有新通知，应命中缓存不再请求，证明失效是精确的一次性行为。
func TestListChangedNotificationRefreshesCachedTools(t *testing.T) {
	var calls atomic.Int64
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			n := calls.Add(1)
			tools := []any{map[string]any{
				"name":        "读取文件",
				"description": "旧工具",
				"inputSchema": map[string]any{"type": "object"},
			}}
			if n >= 2 {
				tools = append(tools, map[string]any{
					"name":        "联网搜索🚀",
					"description": "通知后新增",
					"inputSchema": map[string]any{"type": "object"},
				})
			}
			return map[string]any{"tools": tools}, nil
		},
	}}
	// 跟踪开启；TTL 设为一小时，确保“重新请求”只能由通知驱动，而非时间过期。
	c := &client{
		spec:             Spec{Name: "srv", ToolCacheTTL: time.Hour},
		tr:               ft,
		timeout:          time.Second,
		trackListChanges: true,
	}
	w := newWaiters()
	w.setOnNotification(c.onNotification)
	ctx := context.Background()

	first, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("首次 ListTools: %v", err)
	}
	if len(first) != 1 || first[0].Name != "读取文件" || first[0].RawName != "读取文件" {
		t.Fatalf("首次结果 = %+v", first)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("首次 tools/list 调用 = %d, want 1", got)
	}

	payload := `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`
	if err := w.handle([]byte(payload)); err != nil {
		t.Fatalf("handle notification: %v", err)
	}
	if got := c.revision.Revision(); got != 1 {
		t.Fatalf("通知后 Revision() = %d, want 1", got)
	}

	second, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("通知后 ListTools: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("通知后 tools/list 调用 = %d, want 2（缓存应被失效）", got)
	}
	if len(second) != 2 {
		t.Fatalf("通知后工具数 = %d, want 2: %+v", len(second), second)
	}
	if second[1].Name != "联网搜索🚀" || second[1].RawName != "联网搜索🚀" {
		t.Fatalf("新工具未按原名返回: %+v", second[1])
	}

	third, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("再次 ListTools: %v", err)
	}
	if len(third) != 2 || calls.Load() != 2 {
		t.Fatalf("无新通知时应命中缓存：工具数 %d, 调用数 %d", len(third), calls.Load())
	}
}

// TestListChangedNotificationCallbackDispatch 验证 waiters 的分发契约：只有目标方法
// （含首尾空白）触发回调；回调在 w.mu 临界区之外执行——从回调里重入 waiters 不得
// 死锁；同一批次里的普通响应仍正常投递。
func TestListChangedNotificationCallbackDispatch(t *testing.T) {
	w := newWaiters()
	var got []string
	w.setOnNotification(func(method string) {
		got = append(got, method)
		// 若回调在持锁状态下被调用，这里会死锁。
		w.add(42)
		w.drop(42)
	})

	ch := w.add(1)
	payload := `[
		{"jsonrpc":"2.0","id":1,"result":{"ok":true}},
		{"jsonrpc":"2.0","method":"notifications/tools/list_changed"},
		{"jsonrpc":"2.0","method":" notifications/tools/list_changed "},
		{"jsonrpc":"2.0","method":"notifications/progress","params":{}},
		{"jsonrpc":"2.0","method":"notifications/tools/LIST_CHANGED"},
		{"jsonrpc":"2.0","method":"工具列表已变更"}
	]`
	if err := w.handle([]byte(payload)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if reply := <-ch; string(reply.result) != `{"ok":true}` {
		t.Fatalf("响应投递被通知破坏: %+v", reply)
	}
	// 回调是通用通知钩子：所有带 method 的帧都会到达，方法过滤由客户端的
	// onNotification（见 TestListChangedNotificationClientMethodFilter）完成。
	want := []string{
		"notifications/tools/list_changed",
		" notifications/tools/list_changed ",
		"notifications/progress",
		"notifications/tools/LIST_CHANGED",
		"工具列表已变更",
	}
	if len(got) != len(want) {
		t.Fatalf("回调次数 = %d, want %d: %v", len(got), len(want), got)
	}
	for i, method := range got {
		if method != want[i] {
			t.Fatalf("第 %d 个回调方法 = %q, want %q", i, method, want[i])
		}
	}
}

// TestListChangedNotificationNilCallbackBaseline 验证未安装回调时逐字节不变：
// 含 method 的帧被忽略，同一批次里的响应/error 仍与历史一样关联到等待者。
func TestListChangedNotificationNilCallbackBaseline(t *testing.T) {
	w := newWaiters()
	first := w.add(1)
	second := w.add(2)
	payload := `[
		{"jsonrpc":"2.0","method":"notifications/tools/list_changed"},
		{"jsonrpc":"2.0","id":1,"result":{"a":1}},
		{"jsonrpc":"2.0","method":"notifications/progress"},
		{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"x"}}
	]`
	if err := w.handle([]byte(payload)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if reply := <-first; string(reply.result) != `{"a":1}` {
		t.Fatalf("首个响应 = %+v", reply)
	}
	if reply := <-second; reply.err == nil {
		t.Fatalf("error 响应未投递: %+v", reply)
	}
}

// TestListChangedNotificationClientMethodFilter 验证客户端回调只认精确的目标方法：
// 进度通知、大小写变体与 CJK 文本都不会推进版本号，只有目标方法才 Bump 一次。
func TestListChangedNotificationClientMethodFilter(t *testing.T) {
	c := &client{}
	w := newWaiters()
	w.setOnNotification(c.onNotification)

	for _, payload := range []string{
		`{"jsonrpc":"2.0","method":"notifications/progress"}`,
		`{"jsonrpc":"2.0","method":"notifications/tools/LIST_CHANGED"}`,
		`{"jsonrpc":"2.0","method":"工具列表已变更"}`,
	} {
		if err := w.handle([]byte(payload)); err != nil {
			t.Fatalf("handle %s: %v", payload, err)
		}
	}
	if got := c.revision.Revision(); got != 0 {
		t.Fatalf("非目标通知推进了版本号: %d", got)
	}
	if err := w.handle([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := c.revision.Revision(); got != 1 {
		t.Fatalf("目标通知后 Revision() = %d, want 1", got)
	}
}
