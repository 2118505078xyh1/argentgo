package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAnnotateResult 覆盖四个字段的组合，并确认 Bytes 按字节而非 rune 计数。
func TestAnnotateResult(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		output    string
		sanitized bool
		clipped   bool
		want      Provenance
	}{
		{
			name:   "empty output no flags",
			tool:   "read",
			output: "",
			want:   Provenance{Tool: "read", Bytes: 0},
		},
		{
			name:   "ascii output no flags",
			tool:   "grep",
			output: "abc",
			want:   Provenance{Tool: "grep", Bytes: 3},
		},
		{
			name:      "sanitized only",
			tool:      "bash",
			output:    "hello",
			sanitized: true,
			want:      Provenance{Tool: "bash", Sanitized: true, Bytes: 5},
		},
		{
			name:    "clipped only",
			tool:    "web_fetch",
			output:  "0123456789",
			clipped: true,
			want:    Provenance{Tool: "web_fetch", Clipped: true, Bytes: 10},
		},
		{
			name:      "both flags",
			tool:      "shell",
			output:    "x",
			sanitized: true,
			clipped:   true,
			want:      Provenance{Tool: "shell", Sanitized: true, Clipped: true, Bytes: 1},
		},
		{
			name:   "cjk bytes not runes",
			tool:   "读取文件",
			output: "你好", // 2 个 rune，6 个字节
			want:   Provenance{Tool: "读取文件", Bytes: 6},
		},
		{
			name:   "emoji bytes not runes",
			tool:   "emoji",
			output: "a😀", // 2 个 rune，5 个字节
			want:   Provenance{Tool: "emoji", Bytes: 5},
		},
		{
			name:   "newline in output counts as byte",
			tool:   "read",
			output: "a\nb",
			want:   Provenance{Tool: "read", Bytes: 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AnnotateResult(tc.tool, tc.output, tc.sanitized, tc.clipped)
			if got != tc.want {
				t.Fatalf("AnnotateResult(%q, %q, %v, %v) = %+v, want %+v",
					tc.tool, tc.output, tc.sanitized, tc.clipped, got, tc.want)
			}
		})
	}
}

// TestProvenanceHeader 用精确字符串锁定单行头的字段顺序与格式。
func TestProvenanceHeader(t *testing.T) {
	cases := []struct {
		name string
		p    Provenance
		want string
	}{
		{
			name: "no flags",
			p:    Provenance{Tool: "read", Bytes: 123},
			want: "[tool:read bytes=123]",
		},
		{
			name: "sanitized only",
			p:    Provenance{Tool: "bash", Sanitized: true, Bytes: 7},
			want: "[tool:bash sanitized bytes=7]",
		},
		{
			name: "clipped only",
			p:    Provenance{Tool: "grep", Clipped: true, Bytes: 8},
			want: "[tool:grep clipped bytes=8]",
		},
		{
			name: "both flags fixed order",
			p:    Provenance{Tool: "read", Sanitized: true, Clipped: true, Bytes: 123},
			want: "[tool:read sanitized clipped bytes=123]",
		},
		{
			name: "empty tool and zero bytes",
			p:    Provenance{Bytes: 0},
			want: "[tool: bytes=0]",
		},
		{
			name: "cjk tool preserved",
			p:    Provenance{Tool: "读取文件", Clipped: true, Bytes: 6},
			want: "[tool:读取文件 clipped bytes=6]",
		},
		{
			name: "emoji tool preserved",
			p:    Provenance{Tool: "搜索😀", Sanitized: true, Bytes: 1},
			want: "[tool:搜索😀 sanitized bytes=1]",
		},
		{
			name: "brackets and control chars escaped",
			p:    Provenance{Tool: "a]b\nc\t", Bytes: 2},
			want: "[tool:a\\x5db\\x0ac\\x09 bytes=2]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Header(); got != tc.want {
				t.Fatalf("Header() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProvenanceHeaderStable 验证同一 Provenance 反复调用结果一致。
func TestProvenanceHeaderStable(t *testing.T) {
	p := Provenance{Tool: "读取文件", Sanitized: true, Clipped: true, Bytes: 123}
	want := p.Header()
	for i := 0; i < 100; i++ {
		if got := p.Header(); got != want {
			t.Fatalf("call %d = %q, want stable %q", i, got, want)
		}
	}
}

// TestProvenanceHeaderSingleLineAndUTF8 验证头始终是单行合法 UTF-8，
// 且 CJK 工具名未被切断成非法字节序列。
func TestProvenanceHeaderSingleLineAndUTF8(t *testing.T) {
	tools := []string{"read", "读取文件", "搜索😀", "bad]name\nwith\rtab\t", "含[x]括号"}
	for _, tool := range tools {
		h := (Provenance{Tool: tool, Sanitized: true, Clipped: true, Bytes: 9}).Header()
		if strings.ContainsAny(h, "\n\r") {
			t.Fatalf("Header(%q) is not single-line: %q", tool, h)
		}
		if !utf8.ValidString(h) {
			t.Fatalf("Header(%q) is not valid UTF-8: %q", tool, h)
		}
		if !strings.HasPrefix(h, "[tool:") || !strings.HasSuffix(h, " bytes=9]") {
			t.Fatalf("Header(%q) has unexpected shape: %q", tool, h)
		}
		if strings.Count(h, "[") != 1 || strings.Count(h, "]") != 1 {
			t.Fatalf("Header(%q) has ambiguous brackets: %q", tool, h)
		}
	}
	// CJK 工具名应原样出现在头中（未被转义）。
	const cjk = "读取文件"
	if h := (Provenance{Tool: cjk, Bytes: 1}).Header(); !strings.Contains(h, cjk) {
		t.Fatalf("Header(%q) dropped the CJK tool name: %q", cjk, h)
	}
}

// TestProvenanceHeaderFieldOrder 验证字段顺序固定：sanitized 永远在
// clipped 之前，bytes 永远在末尾，且缺失标记不会引入多余空格。
func TestProvenanceHeaderFieldOrder(t *testing.T) {
	base := Provenance{Tool: "t", Bytes: 1}
	cases := []struct {
		name string
		p    Provenance
	}{
		{"none", base},
		{"sanitized", Provenance{Tool: "t", Sanitized: true, Bytes: 1}},
		{"clipped", Provenance{Tool: "t", Clipped: true, Bytes: 1}},
		{"both", Provenance{Tool: "t", Sanitized: true, Clipped: true, Bytes: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.p.Header()
			if !strings.HasSuffix(h, " bytes=1]") {
				t.Fatalf("bytes not last: %q", h)
			}
			if s, c := strings.Index(h, "sanitized"), strings.Index(h, "clipped"); s >= 0 && c >= 0 && s > c {
				t.Fatalf("sanitized must precede clipped: %q", h)
			}
			if strings.Contains(h, "  ") {
				t.Fatalf("double space in header: %q", h)
			}
		})
	}
}

// TestPrependHeader 验证拼接结果：output 逐字节成为返回值的后缀。
func TestPrependHeader(t *testing.T) {
	cases := []struct {
		name   string
		header string
		output string
		want   string
	}{
		{
			name:   "normal",
			header: "[tool:read bytes=3]",
			output: "abc",
			want:   "[tool:read bytes=3]\nabc",
		},
		{
			name:   "empty output",
			header: "[tool:read bytes=0]",
			output: "",
			want:   "[tool:read bytes=0]\n",
		},
		{
			name:   "empty header returns output",
			header: "",
			output: "abc",
			want:   "abc",
		},
		{
			name:   "both empty",
			header: "",
			output: "",
			want:   "",
		},
		{
			name:   "output with newlines preserved",
			header: "[tool:read bytes=5]",
			output: "a\n\nb\r",
			want:   "[tool:read bytes=5]\na\n\nb\r",
		},
		{
			name:   "cjk content preserved",
			header: "[tool:读取文件 bytes=6]",
			output: "你好世界",
			want:   "[tool:读取文件 bytes=6]\n你好世界",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrependHeader(tc.header, tc.output)
			if got != tc.want {
				t.Fatalf("PrependHeader(%q, %q) = %q, want %q", tc.header, tc.output, got, tc.want)
			}
			if !strings.HasSuffix(got, tc.output) {
				t.Fatalf("output is not a byte-identical suffix: got %q, output %q", got, tc.output)
			}
		})
	}
}

// TestPrependHeaderDoesNotMutateInputs 验证所有函数都不改动入参：
// 字符串保持原值，Provenance 值调用 Header 后字段不变。
func TestPrependHeaderDoesNotMutateInputs(t *testing.T) {
	output := "原始\n内容😀"
	outputBefore := output
	header := "[tool:读取文件 sanitized bytes=13]"
	headerBefore := header

	got := PrependHeader(header, output)
	if output != outputBefore || header != headerBefore {
		t.Fatalf("PrependHeader mutated its inputs: header=%q output=%q", header, output)
	}
	if !strings.Contains(got, outputBefore) || got == outputBefore {
		t.Fatalf("PrependHeader did not prepend: %q", got)
	}

	tool := "read"
	toolBefore := tool
	p := AnnotateResult(tool, outputBefore, true, true)
	if tool != toolBefore || output != outputBefore {
		t.Fatalf("AnnotateResult mutated its inputs: tool=%q output=%q", tool, output)
	}
	before := p
	_ = p.Header()
	_ = p.Header()
	if p != before {
		t.Fatalf("Header mutated its receiver: got %+v, want %+v", p, before)
	}
}
