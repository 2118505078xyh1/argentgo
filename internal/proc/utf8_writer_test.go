package proc

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUTF8CarryWriterJoinsRuneSplitAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	cw := newUTF8CarryWriter(&buf)

	// 中 的编码是 E4 B8 AD；第一次 Write 会暂存一个不完整的前缀。
	if n, err := cw.Write([]byte{0xE4, 0xB8}); n != 2 || err != nil {
		t.Fatalf("first Write = (%d, %v), want (2, nil)", n, err)
	}
	if buf.Len() != 0 {
		t.Fatalf("incomplete prefix must be held back, got %x", buf.Bytes())
	}
	if n, err := cw.Write([]byte{0xAD}); n != 1 || err != nil {
		t.Fatalf("second Write = (%d, %v), want (1, nil)", n, err)
	}
	// 完整的字符在任何 Flush 之前就已到达底层 writer。
	if got := buf.String(); got != "中" {
		t.Fatalf("output = %q, want 中", got)
	}
	cw.Flush()
	if got := buf.String(); got != "中" {
		t.Fatalf("Flush must not duplicate completed data, got %q", got)
	}
}

func TestUTF8CarryWriterPassesASCIIThroughUnchanged(t *testing.T) {
	var buf bytes.Buffer
	cw := newUTF8CarryWriter(&buf)

	in := []byte("plain ASCII output 1234!@#\n")
	if n, err := cw.Write(in); n != len(in) || err != nil {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(in))
	}
	cw.Flush()
	if got := buf.String(); got != string(in) {
		t.Fatalf("output = %q, want %q", got, in)
	}
	if len(cw.carry) != 0 {
		t.Fatalf("carry = %x after Flush, want empty", cw.carry)
	}
}

func TestUTF8CarryWriterMixedTextAtEveryBoundary(t *testing.T) {
	// ASCII、3 字节 CJK、4 字节 emoji 以及尾部 ASCII 尾巴。
	const want = "hello中文world Go语言🌍end"

	// 在每个可能的字节边界处做双向拆分。
	for i := 0; i <= len(want); i++ {
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		if _, err := cw.Write([]byte(want[:i])); err != nil {
			t.Fatalf("split %d first Write: %v", i, err)
		}
		if _, err := cw.Write([]byte(want[i:])); err != nil {
			t.Fatalf("split %d second Write: %v", i, err)
		}
		cw.Flush()
		if got := buf.String(); got != want {
			t.Fatalf("split %d: output = %q, want %q", i, got, want)
		}
	}

	// 一次一个字节：每个字符都由尽可能多的片段组装而成。
	{
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		for i := 0; i < len(want); i++ {
			if _, err := cw.Write([]byte(want[i : i+1])); err != nil {
				t.Fatalf("byte %d Write: %v", i, err)
			}
		}
		cw.Flush()
		if got := buf.String(); got != want {
			t.Fatalf("byte-at-a-time output = %q, want %q", got, want)
		}
	}
}

func TestUTF8CarryWriterFlushDanglingSequence(t *testing.T) {
	var buf bytes.Buffer
	cw := newUTF8CarryWriter(&buf)

	// 一个前导字节加一个续接字节：仍缺最后一个字节。
	if _, err := cw.Write([]byte{0xE4, 0xB8}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	cw.Flush() // must not panic
	if want := []byte{0xE4, 0xB8}; !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("Flush output = %x, want %x (written verbatim)", buf.Bytes(), want)
	}
	// string() 原样复制字节；只有当下游逐字符解码时（JSON、range、[]rune 等），
	// 才会出现 U+FFFD。
	if got := []rune(buf.String()); len(got) != 2 || got[0] != utf8.RuneError || got[1] != utf8.RuneError {
		t.Fatalf("rune decode = %v, want two U+FFFD", got)
	}
	// Flush 是幂等的。
	cw.Flush()
	if !bytes.Equal(buf.Bytes(), []byte{0xE4, 0xB8}) {
		t.Fatalf("second Flush changed output to %x", buf.Bytes())
	}
}

func TestUTF8CarryWriterCapsCarry(t *testing.T) {
	// 在默认上限下，畸形垃圾数据绝不会让暂存内容超过
	// 最大可能的不完整序列（utf8.UTFMax-1 字节）。
	{
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		garbage := bytes.Repeat([]byte{0xF0, 0x9F, 0x98, 0x80, 0x80, 0xC0}, 64)
		for i := 0; i < len(garbage); i++ {
			if _, err := cw.Write(garbage[i : i+1]); err != nil {
				t.Fatalf("Write %d: %v", i, err)
			}
			if len(cw.carry) > maxCarryBytes {
				t.Fatalf("carry grew to %d bytes, cap is %d", len(cw.carry), maxCarryBytes)
			}
		}
		cw.Flush()
	}

	// 强制设置一个小于自然不完整尾部的上限，以演练
	// 截断逻辑：超出的字节会被写出，而不是丢失或被暂存。
	{
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		cw.maxCarry = 1
		in := []byte{0xF0, 0x9F} // 4-byte lead with only one continuation
		if _, err := cw.Write(in); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if len(cw.carry) != 1 {
			t.Fatalf("carry = %x, want exactly 1 byte", cw.carry)
		}
		// 写入底层 writer 的总字节数加上保留的暂存内容
		// 等于输入：截断逻辑没有丢弃任何东西。
		if buf.Len()+len(cw.carry) != len(in) {
			t.Fatalf("lost bytes: buf=%d carry=%d input=%d", buf.Len(), len(cw.carry), len(in))
		}
		cw.Flush()
		if buf.Len() != len(in) {
			t.Fatalf("after Flush buf has %d bytes, want %d", buf.Len(), len(in))
		}
	}
}

func TestUTF8CarryWriterWithCappedBufferPreservesCap(t *testing.T) {
	old := MaxOutputBytes
	MaxOutputBytes = 40
	defer func() { MaxOutputBytes = old }()

	cb := &cappedBuffer{max: MaxOutputBytes}
	cw := newUTF8CarryWriter(cb)
	// 一次一个字节写入重复的多字节字符会超过上限。
	chunk := []byte("中文测试输出")
	for i := 0; i < 16; i++ {
		for j := 0; j < len(chunk); j++ {
			_, _ = cw.Write(chunk[j : j+1])
		}
	}
	cw.Flush()

	if !cb.dropped {
		t.Fatal("output over the cap must be truncated")
	}
	if !strings.Contains(cb.String(), truncatedMarker) {
		t.Fatalf("no truncation marker in %q", cb.String())
	}
	head, tail, found := strings.Cut(cb.String(), truncatedMarker)
	if !found || head == "" || tail == "" {
		t.Fatalf("head/tail truncation broken: %q", cb.String())
	}
	if !strings.Contains(cb.String(), "中") {
		t.Fatalf("CJK rune mangled by truncation: %q", cb.String())
	}
}
