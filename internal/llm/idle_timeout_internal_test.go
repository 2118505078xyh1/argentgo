package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// stagedReader 产出预先构建的 SSE 分块，在第一个之后每块前都暂停。
// 设置 hold 时它会阻塞而非到达 EOF，模拟一个
// 保持打开但静默的连接。
type stagedReader struct {
	chunks  [][]byte
	pause   time.Duration
	hold    bool
	idx     int
	release chan struct{}
	once    sync.Once
}

func newStagedReader(hold bool, pause time.Duration, chunks ...string) *stagedReader {
	r := &stagedReader{hold: hold, pause: pause, release: make(chan struct{})}
	for _, c := range chunks {
		r.chunks = append(r.chunks, []byte(c))
	}
	return r
}

func (r *stagedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		if r.hold {
			<-r.release
			r.hold = false // 释放一次，然后报告 EOF
			return 0, io.EOF
		}
		return 0, io.EOF
	}
	chunk := r.chunks[r.idx]
	r.idx++
	if r.idx > 1 && r.pause > 0 {
		time.Sleep(r.pause)
	}
	return copy(p, chunk), nil
}

func (r *stagedReader) unblock() { r.once.Do(func() { close(r.release) }) }

func sseDelta(content string) string {
	return `data: {"choices":[{"delta":{"content":"` + content + `"}}]}` + "\n\n"
}

const sseDone = "data: [DONE]\n\n"

func TestReadStreamIdleTimeoutFailsSilentConnection(t *testing.T) {
	r := newStagedReader(true, 0, sseDelta("hi"))
	defer r.unblock()

	client := &Client{IdleTimeout: 40 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	msg, err := client.readStream(ctx, "m", r, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrStreamError) {
		t.Fatalf("err = %v, want ErrStreamError", err)
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Fatalf("err = %q, want an idle-timeout message", err)
	}
	if msg.Content != "" {
		t.Fatalf("partial reply leaked on idle timeout: %+v", msg)
	}
	if elapsed > time.Second {
		t.Fatalf("idle timeout took %v, want it well under the 2s context deadline", elapsed)
	}
}

func TestReadStreamIdleTimeoutResetsOnActivity(t *testing.T) {
	// 十个 20ms 间隔共 200ms，超过 150ms 空闲窗口：读取
	// 必须持续成功，因为每个事件都会把截止时间往后推。
	chunks := make([]string, 0, 11)
	for i := 0; i < 10; i++ {
		chunks = append(chunks, sseDelta("x"))
	}
	chunks = append(chunks, sseDone)
	r := newStagedReader(false, 20*time.Millisecond, chunks...)

	client := &Client{IdleTimeout: 150 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, err := client.readStream(ctx, "m", r, nil)
	if err != nil {
		t.Fatalf("Chat readStream: %v", err)
	}
	if msg.Content != "xxxxxxxxxx" {
		t.Fatalf("content = %q, want ten deltas", msg.Content)
	}
}

func TestReadStreamZeroIdleTimeoutWaitsThroughSilence(t *testing.T) {
	// IdleTimeout == 0 必须保持旧行为：等待
	// 250ms 的间隔直到 [DONE]，而不是提前失败。
	r := newStagedReader(false, 250*time.Millisecond, sseDelta("a"), sseDone)

	client := &Client{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	msg, err := client.readStream(ctx, "m", r, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("readStream: %v", err)
	}
	if msg.Content != "a" {
		t.Fatalf("content = %q, want a", msg.Content)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("returned after %v, want to wait through the 250ms gap", elapsed)
	}
}
