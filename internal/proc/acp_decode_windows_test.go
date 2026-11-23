//go:build windows

package proc

import (
	"bytes"
	"testing"
)

func newACPWindowsTestDecoder(codepage uint32) (*acpByteDecoder, *bytes.Buffer) {
	var buf bytes.Buffer
	d := newACPByteDecoder(&buf)
	d.codepage = codepage
	return d, &buf
}

func TestACPDecodeWindowsGBKToChinese(t *testing.T) {
	// 中文 的 GBK(CP936) 编码是 D6 D0 CE C4；D6 D0 已是完整非法 UTF-8
	// 序列，触发回退后整段解码为正确中文。
	d, buf := newACPWindowsTestDecoder(936)
	if _, err := d.Write([]byte{0xD6, 0xD0, 0xCE, 0xC4}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	d.Finish()
	if got := buf.String(); got != "中文" {
		t.Fatalf("output = %q, want 中文", got)
	}
}

func TestACPDecodeWindowsSplitAcrossWrites(t *testing.T) {
	raw := []byte{0xD6, 0xD0, 0xCE, 0xC4, '\n'}
	for split := 0; split <= len(raw); split++ {
		d, buf := newACPWindowsTestDecoder(936)
		if _, err := d.Write(raw[:split]); err != nil {
			t.Fatalf("split %d: Write: %v", split, err)
		}
		if _, err := d.Write(raw[split:]); err != nil {
			t.Fatalf("split %d: Write: %v", split, err)
		}
		d.Finish()
		if got, want := buf.String(), "中文\n"; got != want {
			t.Fatalf("split %d: output = %q, want %q", split, got, want)
		}
	}
}

func TestACPDecodeWindowsValidUTF8Passthrough(t *testing.T) {
	d, buf := newACPWindowsTestDecoder(936)
	in := "hello 中文\n"
	if _, err := d.Write([]byte(in)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := buf.String(); got != in {
		t.Fatalf("valid UTF-8 output = %q, want %q", got, in)
	}
	if d.mode != acpModeUTF8 {
		t.Fatalf("valid UTF-8 must not switch mode, got %v", d.mode)
	}
	// 后续 GBK 字节仍能被正确判定，证明快路径没有破坏状态。
	if _, err := d.Write([]byte{0xD6, 0xD0}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	d.Finish()
	if got, want := buf.String(), in+"中"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestACPDecodeWindowsLatchesACPToEOF(t *testing.T) {
	d, buf := newACPWindowsTestDecoder(936)
	// 先触发回退并解码 中文。
	if _, err := d.Write([]byte{0xD6, 0xD0, 0xCE, 0xC4}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// GBK 一（D2 BB）恰好也是合法 UTF-8（U+04BB）；锁存后必须按 ACP 解码。
	if _, err := d.Write([]byte{0xD2, 0xBB}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	d.Finish()
	if got, want := buf.String(), "中文一"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if d.mode != acpModeACP {
		t.Fatalf("mode must be latched to ACP, got %v", d.mode)
	}
}

func TestACPDecodeWindowsIncompleteTailAtEOFFallsBack(t *testing.T) {
	// CP1252 的 é 是单字节 E9，它也是截断的三字节 UTF-8 前缀；
	// EOF 证明该流不是 UTF-8，应按 ACP 解码。
	d, buf := newACPWindowsTestDecoder(1252)
	if _, err := d.Write([]byte{0xE9}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("incomplete tail must be held before EOF, got %x", buf.Bytes())
	}
	d.Finish()
	if got := buf.String(); got != "é" {
		t.Fatalf("output = %q, want é", got)
	}
}
