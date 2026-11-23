package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

func TestEventLogAppendAndAfter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log := NewEventLog(path)
	for i := 1; i <= 5; i++ {
		if err := log.append(uint64(i), agent.Event{Kind: agent.EventNotice, Text: fmt.Sprintf("e%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	got := log.after(2)
	if len(got) != 3 || got[0].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("after(2) = %+v", got)
	}
	// 损坏行被跳过，不阻塞其余事件。
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("not json\n")
	f.Close()
	got = log.after(4)
	if len(got) != 1 || got[0].Seq != 5 {
		t.Fatalf("after(4) with corrupt line = %+v", got)
	}
}

func TestEventHubPublishWritesLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	hub := NewEventHub()
	hub.AttachEventLog(NewEventLog(path))
	for i := 0; i < 3; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "x"})
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines++
	}
	if lines != 3 {
		t.Fatalf("log lines = %d, want 3", lines)
	}
}

func TestSSELastEventIDReplaysFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	hub := NewEventHub()
	log := NewEventLog(path)
	hub.AttachEventLog(log)

	// 发布超过内存窗口的事件，使最旧的已被淘汰出内存。
	for i := 0; i < eventHistoryCap+10; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: fmt.Sprintf("e%d", i)})
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/event", nil)
	req.Header.Set("Last-Event-ID", "2") // 请求第 3 条起，已超出当前内存窗口
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		NewEventHandler(hub).ServeHTTP(rec, req)
		close(done)
	}()

	// 不能在 handler 写 rec.Body 的同时调用 rec.Body.String() 轮询——
	// httptest.ResponseRecorder 内部是 bytes.Buffer，并发读写会被 race 检测器抓到。
	// 改为等待一小段时间让 handler 回放历史事件，再取消并等待其退出，
	// 之后才单次读取 Body。
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, "id: ") {
		t.Fatalf("no id: frames in SSE output: %s", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "event: server.connected") {
		t.Fatal("missing server.connected frame")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
