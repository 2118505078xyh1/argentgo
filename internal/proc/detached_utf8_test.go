package proc

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSwitchWriterHoldsIncompleteRune 守护分离生成路径的 UTF-8
// 暂存逻辑：exec 按任意分块读取子进程输出，因此一次读取的最后几个字节
// 可能是多字节字符的前缀。Output 的调用方（尤其是提升慢速子进程的
// RunWithTimeout）绝不能观察到解码为 U+FFFD 的悬空序列。
// 自引入捕获以来 Run 一直在暂存字符；StartDetached/RunWithTimeout 背后的
// writer 以前没有这样做。
func TestSwitchWriterHoldsIncompleteRune(t *testing.T) {
	out := newSwitchWriter()

	// "prefix\n" 后跟 中（E4 B8 AD）的前两个字节。
	if _, err := out.Write([]byte("prefix\n\xe4\xb8")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := out.Captured()
	if !utf8.ValidString(got) {
		t.Fatalf("Captured() = %q is not valid UTF-8: a split rune leaked", got)
	}
	if got != "prefix\n" {
		t.Fatalf("Captured() = %q, want the complete prefix only", got)
	}

	// 暂存的字节不会丢失：最后一个字节到达后会发出完整的
	// 字符，EOF 时的 Flush 不会再改变什么。
	if _, err := out.Write([]byte{0xAD}); err != nil {
		t.Fatalf("Write final byte: %v", err)
	}
	out.flush()
	if got := out.Captured(); got != "prefix\n中" {
		t.Fatalf("Captured() = %q, want %q", got, "prefix\n中")
	}
}

// TestSwitchWriterHandoffFlushesPendingRune 确保把正在运行的子进程
// 提升为日志时，不会丢弃那一刻仍不完整的字符。
func TestSwitchWriterHandoffFlushesPendingRune(t *testing.T) {
	out := newSwitchWriter()
	_, _ = out.Write([]byte("a\xe4\xb8")) // "a" 加上 中 的前两个字节

	var dst bytes.Buffer
	out.Handoff(&dst)
	if got := dst.String(); got != "a\xe4\xb8" {
		t.Fatalf("handoff = %q, want the captured prefix and dangling bytes verbatim", got)
	}

	// 替换之后，写入仍会暂存不完整的字符，只是现在写入日志。
	_, _ = out.Write([]byte{0xAD})
	out.flush()
	if got := dst.String(); got != "a中" {
		t.Fatalf("log = %q, want %q", got, "a中")
	}
}

const detachedUTF8Marker = "ARGENT_PROC_DETACHED_UTF8_HELPER"

// TestDetachedUTF8Helper 是一个重新执行的测试二进制，它把一个字符分
// 两部分写出，并等待父进程释放第二部分。这使父进程能在字符真正跨
// 管道读取被拆开时观察到输出。
func TestDetachedUTF8Helper(t *testing.T) {
	if os.Getenv(detachedUTF8Marker) == "" {
		return
	}
	_, _ = os.Stdout.Write([]byte("detached-utf8-ready\n"))
	_, _ = os.Stdout.Write([]byte{0xE4, 0xB8}) // prefix of 中
	var b [1]byte
	_, _ = os.Stdin.Read(b[:]) // 等待父进程检查 Output
	_, _ = os.Stdout.Write([]byte{0xAD})
}

// TestDetachedOutputHoldsSplitRuneUntilComplete 演练真实的 OS 路径：
// 卡在字符中间的子进程不得让 Process.Output 成为非法 UTF-8，且
// 子进程完成后必须出现完整的字符。
func TestDetachedOutputHoldsSplitRuneUntilComplete(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	p, err := StartDetached(Command{
		Path:  os.Args[0],
		Args:  []string{"-test.run=^TestDetachedUTF8Helper$"},
		Env:   []string{detachedUTF8Marker + "=1"},
		Stdin: pr,
	})
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(p.Output(), "detached-utf8-ready\n") {
			break
		}
		if time.Now().After(deadline) {
			_, _ = pw.Write([]byte("x"))
			t.Fatal("helper never produced its ready marker")
		}
		time.Sleep(5 * time.Millisecond)
	}

	mid := p.Output()
	if !utf8.ValidString(mid) {
		t.Errorf("Output() mid-rune = %q, want valid UTF-8", mid)
	}
	if strings.ContainsRune(mid, utf8.RuneError) {
		t.Errorf("Output() mid-rune = %q, want no U+FFFD", mid)
	}

	_, _ = pw.Write([]byte("x"))
	if code := p.Wait(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out := p.Output(); !strings.Contains(out, "中") {
		t.Fatalf("final output = %q, want the completed rune", out)
	}
}
