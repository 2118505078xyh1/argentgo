package lsp

import (
	"sync"
	"sync/atomic"
	"testing"
)

// muxNopWriteCloser 让假客户端的 shutdown/exit 写入立即成功，避免测试等待真实进程。
type muxNopWriteCloser struct{}

func (muxNopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (muxNopWriteCloser) Close() error                { return nil }

// newMuxFakeClient 构造一个可安全且快速 Close 的客户端桩。
// 预置 err 使 shutdown 调用立即返回；readDone 已关闭使 Close 不等待 reader 超时。
// wait 回调递增 closed，用来观察客户端是否真的被关闭过一次。
func newMuxFakeClient(closed *int32) *Client {
	readDone := make(chan struct{})
	close(readDone)
	return &Client{
		err:      ErrClosed,
		stdin:    muxNopWriteCloser{},
		pending:  make(map[uint64]chan response),
		doneCh:   make(chan struct{}),
		readDone: readDone,
		wait: func() error {
			atomic.AddInt32(closed, 1)
			return nil
		},
	}
}

func TestMuxClientForFileRoutesByLanguage(t *testing.T) {
	m := NewMux()
	goClient := &Client{}
	rustClient := &Client{}
	tsClient := &Client{}

	m.Add("gopls", []string{"go"}, goClient)
	m.Add("rust-analyzer", []string{"rust"}, rustClient)
	m.Add("typescript", []string{"typescript", "typescriptreact", "javascript", "javascriptreact"}, tsClient)

	cases := []struct {
		path string
		want *Client
	}{
		{path: "main.go", want: goClient},
		{path: "lib.rs", want: rustClient},
		{path: "index.ts", want: tsClient},
		{path: "view.tsx", want: tsClient},
		{path: "app.js", want: tsClient},
		{path: "app.jsx", want: tsClient},
	}
	for _, tc := range cases {
		got, ok := m.ClientForFile(tc.path)
		if !ok || got != tc.want {
			t.Errorf("ClientForFile(%q) = %p, %v; want %p, true", tc.path, got, ok, tc.want)
		}
	}
	if got, ok := m.ClientForFile("notes.txt"); ok || got != nil {
		t.Errorf("ClientForFile(unsupported) = %p, %v; want nil, false", got, ok)
	}
	if m.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", m.Len())
	}
}

func TestMuxClientForLanguageNormalizesAndFindsClients(t *testing.T) {
	m := NewMux()
	multi := &Client{}
	m.Add("typescript", []string{" TypeScript ", "javascript", "", "typescript"}, multi)

	for _, lang := range []string{"typescript", "TYPESCRIPT", "  JavaScript  "} {
		got, ok := m.ClientForLanguage(lang)
		if !ok || got != multi {
			t.Errorf("ClientForLanguage(%q) = %p, %v; want multi client", lang, got, ok)
		}
	}
	if got, ok := m.ClientForLanguage("go"); ok || got != nil {
		t.Errorf("ClientForLanguage(go) = %p, %v; want nil, false", got, ok)
	}
	// 语言去重后只应保留一条绑定，重复的语言 id 不会造成多余的路由项。
	if m.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", m.Len())
	}
}

func TestMuxFirstRegisteredWinsAndFallsBackAfterRemove(t *testing.T) {
	m := NewMux()
	first := &Client{}
	second := &Client{}

	m.Add("first", []string{"go"}, first)
	m.Add("second", []string{"go"}, second)
	if got, ok := m.ClientForLanguage("go"); !ok || got != first {
		t.Fatalf("first registered client should win, got %p, %v", got, ok)
	}

	// 首选被移除后，后注册的 client 自动成为新的首选，结果仍确定。
	m.Remove("first")
	if got, ok := m.ClientForLanguage("go"); !ok || got != second {
		t.Fatalf("after Remove(first) want second, got %p, %v", got, ok)
	}
	m.Remove("second")
	if got, ok := m.ClientForLanguage("go"); ok || got != nil {
		t.Fatalf("after Remove(second) want nil, false, got %p, %v", got, ok)
	}
}

func TestMuxRemoveClearsAllBindingsOfServer(t *testing.T) {
	m := NewMux()
	multi := &Client{}
	other := &Client{}
	m.Add("typescript", []string{"typescript", "javascript"}, multi)
	m.Add("gopls", []string{"go"}, other)

	m.Remove("typescript")
	for _, lang := range []string{"typescript", "javascript"} {
		if got, ok := m.ClientForLanguage(lang); ok || got != nil {
			t.Errorf("after Remove, ClientForLanguage(%q) = %p, %v; want nil, false", lang, got, ok)
		}
	}
	if got, ok := m.ClientForLanguage("go"); !ok || got != other {
		t.Errorf("Remove must not affect other servers, got %p, %v", got, ok)
	}
	if m.Len() != 1 {
		t.Fatalf("Len() after Remove = %d, want 1", m.Len())
	}

	// 移除不存在的 server 是安全的空操作。
	m.Remove("missing")
	if m.Len() != 1 {
		t.Fatalf("Len() after removing missing server = %d, want 1", m.Len())
	}
}

func TestMuxAddReplacesExistingServerBinding(t *testing.T) {
	m := NewMux()
	oldClient := &Client{}
	newClient := &Client{}
	m.Add("gopls", []string{"go"}, oldClient)
	m.Add("gopls", []string{"rust"}, newClient)

	if got, ok := m.ClientForLanguage("go"); ok || got != nil {
		t.Errorf("stale binding must be removed, got %p, %v", got, ok)
	}
	if got, ok := m.ClientForLanguage("rust"); !ok || got != newClient {
		t.Errorf("new binding missing, got %p, %v", got, ok)
	}
	if m.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", m.Len())
	}
}

func TestMuxCloseDedupsAndIsConcurrentSafe(t *testing.T) {
	m := NewMux()
	var closedA, closedB, closedC int32
	a := newMuxFakeClient(&closedA)
	b := newMuxFakeClient(&closedB)
	c := newMuxFakeClient(&closedC)

	// 同一个 client A 注册在两个 serverName 下，Close 时只能关闭一次。
	m.Add("a1", []string{"go"}, a)
	m.Add("a2", []string{"rust"}, a)
	m.Add("b", []string{"typescript"}, b)
	m.Add("c", []string{"python"}, c)
	if m.Len() != 4 {
		t.Fatalf("Len() = %d, want 4", m.Len())
	}

	// 并发调用 Close：只有一个 goroutine 拿到客户端集合，不应出现重复关闭或 panic。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Close()
		}()
	}
	wg.Wait()

	for name, count := range map[string]*int32{"a": &closedA, "b": &closedB, "c": &closedC} {
		if got := atomic.LoadInt32(count); got != 1 {
			t.Errorf("client %s closed %d times, want exactly once", name, got)
		}
	}
	if m.Len() != 0 {
		t.Fatalf("Len() after Close = %d, want 0", m.Len())
	}
	if got, ok := m.ClientForLanguage("go"); ok || got != nil {
		t.Errorf("routing after Close = %p, %v; want nil, false", got, ok)
	}
	// 重复 Close 不应再次触碰任何客户端。
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := atomic.LoadInt32(&closedA) + atomic.LoadInt32(&closedB) + atomic.LoadInt32(&closedC); got != 3 {
		t.Fatalf("clients closed after second Close = %d, want 3", got)
	}
}
