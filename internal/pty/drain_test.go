package pty

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// chunkReader 每次 Read 最多返回 chunk 个字节，用于模拟 PTY 的任意分块边界。
type chunkReader struct {
	data  []byte
	chunk int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.chunk
	if n > len(r.data) {
		n = len(r.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestCopySanitizedStripsTerminalQueries(t *testing.T) {
	// DSR/DA/DECRQSS/OSC 查询横跨多个 Read 块也必须被移除；合法的 SGR
	// 颜色序列与其它文本逐字节保留。
	input := "start\x1b[6nmid\x1b[c\x1b[$q\x1b]10;?\x07end\x1b[31mred\x1b[0m"
	for _, chunk := range []int{1, 2, 3, 7, 4096} {
		var out bytes.Buffer
		total, truncated, err := copySanitized(&out, &chunkReader{data: []byte(input), chunk: chunk}, 0)
		if err != nil {
			t.Fatalf("chunk=%d: %v", chunk, err)
		}
		want := "startmidend\x1b[31mred\x1b[0m"
		if got := out.String(); got != want {
			t.Fatalf("chunk=%d: got %q, want %q", chunk, got, want)
		}
		if total != int64(len(want)) || truncated {
			t.Fatalf("chunk=%d: total=%d truncated=%v", chunk, total, truncated)
		}
	}
}

func TestCopySanitizedKeepsUTF8RuneAcrossChunks(t *testing.T) {
	input := "你\u4f60\u597d好"
	var out bytes.Buffer
	total, _, err := copySanitized(&out, &chunkReader{data: []byte(input), chunk: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != input {
		t.Fatalf("got %q, want %q", got, input)
	}
	if !utf8.Valid(out.Bytes()) || total != int64(len(input)) {
		t.Fatalf("invalid UTF-8 or wrong byte count: total=%d", total)
	}
}

func TestCopySanitizedClipsToByteLimit(t *testing.T) {
	var out bytes.Buffer
	total, truncated, err := copySanitized(&out, strings.NewReader(strings.Repeat("a", 40)+"\x1b[6n"), 12)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("expected truncation")
	}
	got := out.String()
	if strings.Contains(got, "\x1b[6n") {
		t.Fatalf("query leaked into clipped output: %q", got)
	}
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "aaaa") || !strings.Contains(got, clipMarker) {
		t.Fatalf("unexpected clipped output: %q", got)
	}
	if total != int64(len(got)) {
		t.Fatalf("total=%d, len=%d", total, len(got))
	}
}

// TestPTYHelperProcess 是 Run 集成测试在子进程里执行的目标：按环境变量
// 决定是打印带终端查询的文本，还是永久阻塞以验证取消能杀死子进程。
func TestPTYHelperProcess(t *testing.T) {
	switch os.Getenv("ARGENT_PTY_TEST_HELPER") {
	case "query":
		fmt.Print("START\x1b[6nEND")
	case "block":
		select {}
	}
}

func helperCommand(mode string) Command {
	return Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestPTYHelperProcess$"},
		Env:  []string{"ARGENT_PTY_TEST_HELPER=" + mode},
	}
}

func TestRunFiltersQueriesFromRealPTY(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported on this platform")
	}
	var out bytes.Buffer
	result, err := Run(context.Background(), helperCommand("query"), DefaultSize, nil, &out, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	got := out.String()
	if strings.Contains(got, "\x1b[6n") {
		t.Fatalf("DSR query leaked through the real PTY: %q", got)
	}
	if !strings.Contains(got, "STARTEND") {
		t.Fatalf("child output missing from PTY stream: %q", got)
	}
}

func TestRunCancelClosesChild(t *testing.T) {
	if !Supported() {
		t.Skip("pseudoterminals are not supported on this platform")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, helperCommand("block"), DefaultSize, nil, &out, 0)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error after cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
