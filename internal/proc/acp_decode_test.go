package proc

import (
	"bytes"
	"testing"
)

func TestACPDecodeEnabledGate(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{name: "empty", val: "", want: true},
		{name: "zero", val: "0", want: false},
		{name: "one", val: "1", want: true},
		{name: "padded", val: " 1 ", want: true},
		{name: "word", val: "true", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(acpDecodeEnv, tc.val)
			if got := acpDecodeEnabled(); got != tc.want {
				t.Fatalf("acpDecodeEnabled() with %q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestACPDecodeNoFallbackKeepsReplacement(t *testing.T) {
	// codepage=0 模拟非 Windows：没有 ACP 回退，非法字节仍按 UTF-8 替换呈现。
	var buf bytes.Buffer
	d := newACPByteDecoder(&buf)
	d.codepage = 0

	in := append([]byte("ok "), 0xff, 0x81)
	if _, err := d.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := d.Write([]byte(" 中文")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	d.Finish()
	if got, want := buf.String(), "ok \uFFFD\uFFFD 中文"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestACPDecodeHoldsIncompleteRuneTail(t *testing.T) {
	var buf bytes.Buffer
	d := newACPByteDecoder(&buf)
	d.codepage = 0

	// 中的首字节 E4 是不完整前缀，必须暂存而不是立刻判为非法。
	if _, err := d.Write([]byte{0xE4}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("incomplete prefix must be held, got %x", buf.Bytes())
	}
	if _, err := d.Write([]byte{0xB8, 0xAD}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	d.Finish()
	if got := buf.String(); got != "中" {
		t.Fatalf("output = %q, want 中", got)
	}
}
