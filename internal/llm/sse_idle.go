package llm

import (
	"context"
	"time"
)

// sseIdleReader 在共享 SSE 读取器之上提供逐事件的空闲超时检测。
//
// timeout <= 0 时直接同步调用底层 reader.next()：零 goroutine、零定时器，
// 请求/SSE/回复字节与旧路径完全一致。
// timeout > 0 时把阻塞的读取放到 goroutine 并用定时器监视；每个成功到达的
// 事件都会把截止时间往后推，静默则以可重试的 streamError 失败。
//
// 三个协议客户端共享这一实现，避免 chat 独有的 idle 能力再次漂移到其他协议。
type sseIdleReader struct {
	reader  *sseReader
	timeout time.Duration

	idle    *time.Timer
	idleC   <-chan time.Time
	reads   chan sseIdleReadResult
	started bool
}

// sseIdleReadResult 是后台读取协程回传的一次读取结果。
type sseIdleReadResult struct {
	data string
	ok   bool
	err  error
}

// newSSEIdleReader 构造带空闲检测的 SSE 读取器。调用方必须在读取结束后
// 调用 stop 释放定时器。
func newSSEIdleReader(reader *sseReader, timeout time.Duration) *sseIdleReader {
	r := &sseIdleReader{reader: reader, timeout: timeout}
	if timeout > 0 {
		r.idle = time.NewTimer(timeout)
		r.idleC = r.idle.C
		r.reads = make(chan sseIdleReadResult, 1) // 带缓冲：迟到的读取也总能退出
	}
	return r
}

// stop 释放空闲定时器；同步路径下是空操作。
func (r *sseIdleReader) stop() {
	if r.idle != nil {
		r.idle.Stop()
	}
}

// next 读取下一个 SSE 事件。返回的错误已按协议约定包装完毕，调用方直接返回即可。
func (r *sseIdleReader) next(ctx context.Context) (string, bool, error) {
	if r.idle == nil {
		next, ok, err := r.reader.next()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", false, ctxErr
			}
			return "", false, streamError("read stream: %v", RedactError(err))
		}
		return next, ok, nil
	}

	if !r.started {
		go func() {
			next, ok, err := r.reader.next()
			r.reads <- sseIdleReadResult{data: next, ok: ok, err: err}
		}()
		r.started = true
	}
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case <-r.idleC:
		return "", false, streamError("stream idle for %s with no data", r.timeout)
	case res := <-r.reads:
		r.started = false
		if res.err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", false, ctxErr
			}
			return "", false, streamError("read stream: %v", RedactError(res.err))
		}
		if !res.ok {
			return "", false, nil
		}
		// 任何到达的事件都会把静默截止时间往后推。
		r.resetIdle()
		return res.data, true, nil
	}
}

// resetIdle 把静默截止时间重置为完整的一个 IdleTimeout 窗口。
func (r *sseIdleReader) resetIdle() {
	if !r.idle.Stop() {
		select {
		case <-r.idle.C:
		default:
		}
	}
	r.idle.Reset(r.timeout)
}
