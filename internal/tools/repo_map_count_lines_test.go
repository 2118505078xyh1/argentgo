package tools

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCountLinesStreamingCases 覆盖流式计数的各种输入形态：
// 空输入、末行有无换行、CRLF、纯 CJK 字节以及超过分块大小的大输入。
func TestCountLinesStreamingCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"空输入", "", 0},
		{"单行无换行", "a", 1},
		{"单行有换行", "a\n", 1},
		{"两行末行无换行", "a\nb", 2},
		{"两行末行有换行", "a\nb\n", 2},
		{"仅一个换行", "\n", 1},
		{"仅两个换行", "\n\n", 2},
		{"CRLF 结尾", "a\r\nb\r\n", 2},
		{"CRLF 末行无换行", "a\r\nb", 2},
		{"仅 CR 不视为换行", "a\rb", 1},
		{"单独 CRLF", "\r\n", 1},
		{"CJK 带结尾换行", "你好\n世界\n", 2},
		{"CJK 末行无换行", "你好\n世界", 2},
		{"CJK 单行无换行", "你好世界", 1},
		{"超过分块的多行", strings.Repeat("x\n", countLinesChunkSize), countLinesChunkSize},
		{"超过分块的单行 CJK", strings.Repeat("界", countLinesChunkSize), 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := countLinesStreaming(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("countLinesStreaming() 返回错误: %v", err)
			}
			if got != tc.want {
				t.Fatalf("countLinesStreaming() = %d, 期望 %d", got, tc.want)
			}
		})
	}
}

// oneByteReader 每次只返回一个字节，用于强制分块边界落在任意位置，
// 包括把一个 CRLF 对或一个 CJK 字符切开的情形。
type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

// TestCountLinesStreamingByteAtATime 验证末字节追踪跨读取调用仍然正确。
func TestCountLinesStreamingByteAtATime(t *testing.T) {
	for _, input := range []string{"", "a", "a\n", "a\r\nb", "你好\r\n世界", "界"} {
		want, err := countLinesStreaming(strings.NewReader(input))
		if err != nil {
			t.Fatalf("基准计数失败: %v", err)
		}
		got, err := countLinesStreaming(&oneByteReader{data: []byte(input)})
		if err != nil {
			t.Fatalf("逐字节计数失败: %v", err)
		}
		if got != want {
			t.Fatalf("输入 %q 逐字节计数 = %d, 期望 %d", input, got, want)
		}
	}
}

// eofWithDataReader 在同一次 Read 中同时返回数据和 EOF。
type eofWithDataReader struct {
	data []byte
	done bool
}

func (r *eofWithDataReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, r.data), io.EOF
}

// TestCountLinesStreamingEOFWithData 验证数据伴随 EOF 返回时不会漏数末行。
func TestCountLinesStreamingEOFWithData(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"", 0},
		{"你好", 1},
	} {
		got, err := countLinesStreaming(&eofWithDataReader{data: []byte(tc.input)})
		if err != nil {
			t.Fatalf("countLinesStreaming() 返回错误: %v", err)
		}
		if got != tc.want {
			t.Fatalf("输入 %q 计数 = %d, 期望 %d", tc.input, got, tc.want)
		}
	}
}

// TestCountLinesStreamingError 验证读取失败时返回错误且不返回部分计数。
func TestCountLinesStreamingError(t *testing.T) {
	sentinel := errors.New("读取失败")
	r := &errorReader{data: []byte("a\nb\n"), err: sentinel}
	got, err := countLinesStreaming(r)
	if !errors.Is(err, sentinel) {
		t.Fatalf("countLinesStreaming() 错误 = %v, 期望 %v", err, sentinel)
	}
	if got != 0 {
		t.Fatalf("出错时计数 = %d, 期望 0", got)
	}
}

// errorReader 先返回一段数据，随后返回指定错误。
type errorReader struct {
	data []byte
	pos  int
	err  error
}

func (r *errorReader) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, r.err
}

// TestCountLinesFromFile 验证基于路径的 countLines 走通流式实现，
// 并保持原有的文件语义（缺失文件返回 0）。
func TestCountLinesFromFile(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"empty.txt", "", 0},
		{"no_newline.txt", "a\nb", 2},
		{"crlf.txt", "a\r\nb\r\n", 2},
		{"cjk.txt", "你好\n世界", 2},
		{"large.txt", strings.Repeat("x\n", countLinesChunkSize), countLinesChunkSize},
	}
	for _, tc := range cases {
		path := filepath.Join(root, tc.name)
		if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}
		if got := countLines(path); got != tc.want {
			t.Fatalf("countLines(%q) = %d, 期望 %d", tc.name, got, tc.want)
		}
	}
	if got := countLines(filepath.Join(root, "missing.txt")); got != 0 {
		t.Fatalf("缺失文件的 countLines = %d, 期望 0", got)
	}
}
