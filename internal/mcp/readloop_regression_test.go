package mcp

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// readLoopHarness 构建一个其 readLoop 无需活子进程即可运行的传输：
// cmd.Process 为 nil，因此 finish 跳过了杀死操作，stdin 是一个
// 管道写端，因此 finish 可以关闭它。
func readLoopHarness() *stdioTransport {
	_, stdin := io.Pipe()
	return &stdioTransport{
		name:    "oversize",
		cmd:     &exec.Cmd{},
		stdin:   stdin,
		waiters: newWaiters(),
		diag:    &ringBuffer{},
		done:    make(chan struct{}),
	}
}

// countingReader 记录消费者实际从源中拉取了多少字节。
// 它自身没有上限，因此可以暴露一个把无界行全部读进内存、而不是
// 在帧上限处停止的消费者。
type countingReader struct {
	src  io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += n
	return n, err
}

// TestReadLoopDoesNotDrainOversizeLine 是针对帧上限的回归测试：
// 一台发出没有换行的巨行的服务器，不得让 readLoop 在拒绝它
// 之前读完（并缓冲）整行。之前使用的 ReadBytes 会一直累积
// 直到 EOF，因此恶意服务器能迫使内存增长远超 maxLineBytes。
// 有界的读取器会在距离上限一个缓冲区块之内
// 停止。
func TestReadLoopDoesNotDrainOversizeLine(t *testing.T) {
	huge := append(bytes.Repeat([]byte("x"), maxLineBytes*2), '\n')
	counter := &countingReader{src: bytes.NewReader(huge)}

	tpt := readLoopHarness()
	tpt.readLoop(counter)

	if counter.read > maxLineBytes+256*1024 {
		t.Fatalf("readLoop consumed %d bytes for an oversize frame; it should stop near the %d byte cap",
			counter.read, maxLineBytes)
	}
	if !strings.Contains(tpt.closedError("").Error(), "larger than") {
		t.Fatalf("oversize frame was not reported: %v", tpt.closedError(""))
	}
}
