package pty

import (
	"context"
	"fmt"
	"io"
	"unicode/utf8"

	"example.com/argentgo/internal/proc"
)

// clipMarker 是输出触及字节上限时附加的截断标记，与 tools/bash 侧的标记保持一致。
const clipMarker = "\n... [truncated] ...\n"

// RunResult 是一次交互式终端会话的汇总。
type RunResult struct {
	ExitCode    int   // 子进程退出码；被信号终止或无法回收时为 -1
	OutputBytes int64 // 实际写入 out 的字节数（含截断标记）
	Truncated   bool  // 输出是否因触及上限被裁剪
}

// emptyReader 是 stdin 未提供时的空源，让调用方无需为 nil 分支操心。
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

// Run 在伪终端内启动 cmd，把子进程输出经安全过滤后写入 out，并把 stdin 的
// 字节转发给终端。
//
// 读循环串接 queryFilter（移除 DSR/DA/DECRQSS/OSC 查询，防止终端回答变成
// 提示注入通道）→ UTF-8 进位 → 字节裁剪，再交给 out；这是 TODO #29 要求的
// 「过滤器必须真正运行」。ctx 取消会终止子进程并关闭终端（Start 已绑定），
// 因此调用方可直接把 Ctrl-C 对应的 context 传进来。
func Run(ctx context.Context, cmd Command, size Size, stdin io.Reader, out io.Writer, limit int) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdin == nil {
		stdin = emptyReader{}
	}
	if out == nil {
		return RunResult{}, fmt.Errorf("pty: nil output writer")
	}

	p, err := Start(ctx, cmd, size)
	if err != nil {
		return RunResult{}, err
	}
	defer p.Close()

	// 并行回收子进程：Windows 上 ConPTY 在 Wait 释放控制台后输出管道才会
	// EOF，若不在另一路等待，读循环会永久阻塞。
	waitDone := make(chan struct{})
	var exitCode int
	var waitErr error
	go func() {
		exitCode, waitErr = p.Wait()
		close(waitDone)
	}()

	// 转发 stdin：终端被关闭或 ctx 取消后 p.Write 会失败，goroutine 随即退出。
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stdin.Read(buf)
			if n > 0 {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, writeErr := p.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	written, truncated, copyErr := copySanitized(out, p, limit)
	<-waitDone
	result := RunResult{ExitCode: exitCode, OutputBytes: written, Truncated: truncated}
	if copyErr != nil {
		return result, copyErr
	}
	if waitErr != nil {
		return result, waitErr
	}
	return result, nil
}

// copySanitized 把 r 的字节经安全过滤后写入 w，返回写入字节数、是否裁剪与错误。
// 它读至 EOF（子进程退出/终端关闭），因此会自然排空 PTY 的缓冲输出。
func copySanitized(w io.Writer, r io.Reader, limit int) (int64, bool, error) {
	sink := &clipSink{w: w, limit: limit}
	var filter queryFilter
	var carry []byte
	buf := make([]byte, 4096)

	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			data := filter.Write(buf[:n])
			if len(carry) > 0 {
				data = append(append([]byte(nil), carry...), data...)
			}
			var complete []byte
			complete, carry = splitUTF8Tail(data)
			sink.write(complete)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return sink.total, sink.truncated, readErr
			}
			break
		}
	}

	// 冲刷：查询过滤器挂起的 ESC 片段不可能是完整查询，UTF-8 进位在 EOF 处
	// 也原样写出，保证收尾字节不被吞掉。
	tail := append(filter.Flush(), carry...)
	sink.write(tail)
	return sink.total, sink.truncated, sink.err
}

// splitUTF8Tail 把一个块拆成可安全发出的完整前缀与必须留到下一块的 UTF-8
// 尾部进位。一个 rune 最多 4 字节，因此只需回看最后 3 个字节。
func splitUTF8Tail(data []byte) (complete []byte, carry []byte) {
	for k := len(data) - 1; k >= 0 && k >= len(data)-3; k-- {
		if utf8.RuneStart(data[k]) && !utf8.FullRune(data[k:]) {
			return data[:k], append([]byte(nil), data[k:]...)
		}
	}
	return data, nil
}

// clipSink 在字节预算内写出数据；首个写错误被记录并中止后续写入。
type clipSink struct {
	w         io.Writer
	limit     int
	total     int64
	truncated bool
	err       error
}

// write 把 p 写入底层 w，超过 limit 时只保留不超过预算的完整 rune 前缀并附加截断标记。
func (s *clipSink) write(p []byte) {
	if s.err != nil || len(p) == 0 {
		return
	}
	if s.limit > 0 && s.total+int64(len(p)) > int64(s.limit) {
		room := int64(s.limit) - s.total
		if room > 0 {
			head, _ := splitUTF8Tail(p[:int(room)])
			n, err := s.w.Write(head)
			s.total += int64(n)
			if err != nil {
				s.err = err
				return
			}
		}
		n, err := s.w.Write([]byte(clipMarker))
		s.total += int64(n)
		if err != nil {
			s.err = err
			return
		}
		s.truncated = true
		return
	}
	n, err := s.w.Write(p)
	s.total += int64(n)
	if err != nil {
		s.err = err
	}
}

// sanitizedEnv 复用 proc.SanitizeEnv 处理子进程环境：去掉宿主私有变量，
// 再追加调用方显式传入的条目。平台 startPlatform 统一通过它构造环境。
func sanitizedEnv(extra []string) []string {
	return proc.SanitizeEnv(extra)
}
