package mcp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadBoundedLineSplitsFrames(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("first\nsecond\nthird\n"), 8)
	for _, want := range []string{"first\n", "second\n", "third\n"} {
		line, err := readBoundedLine(reader, 64)
		if err != nil {
			t.Fatalf("readBoundedLine: %v", err)
		}
		if string(line) != want {
			t.Fatalf("line = %q, want %q", line, want)
		}
	}
	if _, err := readBoundedLine(reader, 64); !errors.Is(err, io.EOF) {
		t.Fatalf("after last frame: err = %v, want io.EOF", err)
	}
}

func TestReadBoundedLineReturnsFinalLineAtEOF(t *testing.T) {
	// 没有尾随换行的最后一行会与 io.EOF 一起被交付。
	reader := bufio.NewReaderSize(strings.NewReader("tail"), 4)
	line, err := readBoundedLine(reader, 64)
	if string(line) != "tail" || !errors.Is(err, io.EOF) {
		t.Fatalf("line = %q err = %v, want %q and io.EOF", line, err, "tail")
	}
}

func TestReadBoundedLineRejectsOversizeWithoutDraining(t *testing.T) {
	const limit = 16
	// 读取器的缓冲区相对输入很小，迫使出现多次
	// ErrBufferFull 循环；辅助函数必须在超过上限时立即停止。
	counter := &countingReader{src: strings.NewReader(strings.Repeat("x", 4096))}
	reader := bufio.NewReaderSize(counter, 8)

	line, err := readBoundedLine(reader, limit)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("err = %v, want errLineTooLong", err)
	}
	if len(line) <= limit {
		t.Fatalf("line length = %d, want > %d", len(line), limit)
	}
	// bufio 强制最小 16 字节缓冲区，因此单个 ReadSlice 块可能
	// 有 16 字节；上限检查容许一个这样的块超出。
	if counter.read > limit+16 {
		t.Fatalf("read %d bytes, want at most %d (cap %d + one buffer)", counter.read, limit+16, limit)
	}
}
