package llm_test

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// boundedMarker 与实现中的截断标记保持一致（实现未导出该常量）。
const boundedMarker = "...[truncated]..."

// TestBoundedBufferUnderLimit 覆盖未超限场景：空、ASCII、CJK、emoji、
// 跨多次 Write 的半个 rune，以及写入量恰好等于上限的边界（不应截断）。
func TestBoundedBufferUnderLimit(t *testing.T) {
	halfRune := []byte("你")
	cases := []struct {
		name   string
		limit  int
		writes [][]byte
		want   string
	}{
		{
			name:  "empty buffer",
			limit: 16,
			want:  "",
		},
		{
			name:   "ascii under limit",
			limit:  16,
			writes: [][]byte{[]byte("hello "), []byte("world")},
			want:   "hello world",
		},
		{
			name:   "cjk under limit",
			limit:  64,
			writes: [][]byte{[]byte("你好"), []byte("世界")},
			want:   "你好世界",
		},
		{
			name:   "emoji under limit",
			limit:  64,
			writes: [][]byte{[]byte("🚀🚀")},
			want:   "🚀🚀",
		},
		{
			name:   "rune split across writes stays intact",
			limit:  64,
			writes: [][]byte{halfRune[:2], halfRune[2:]},
			want:   "你",
		},
		{
			name:   "exactly at limit is not truncated",
			limit:  8,
			writes: [][]byte{[]byte("12345678")},
			want:   "12345678",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := llm.NewBoundedBuffer(tc.limit)
			for _, w := range tc.writes {
				before := append([]byte(nil), w...)
				b.Write(w)
				if string(w) != string(before) {
					t.Errorf("Write 修改了入参: %q -> %q", before, w)
				}
			}
			if b.Truncated() {
				t.Fatal("未超限却报告 Truncated=true")
			}
			if got := string(b.Bytes()); got != tc.want {
				t.Errorf("Bytes() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBoundedBufferTruncatedHeadTail 验证超限后保留流头部与流尾部，
// 中间以截断标记连接；同时验证单次大写入与逐字节写入结果一致。
func TestBoundedBufferTruncatedHeadTail(t *testing.T) {
	const limit = 40
	content := strings.Repeat("a", 50) + strings.Repeat("b", 50)
	want := strings.Repeat("a", 10) + boundedMarker + strings.Repeat("b", 10)

	cases := []struct {
		name   string
		writes [][]byte
	}{
		{
			name:   "single large write",
			writes: [][]byte{[]byte(content)},
		},
		{
			name:   "split into chunks",
			writes: [][]byte{[]byte(content[:30]), []byte(content[30:70]), []byte(content[70:])},
		},
		{
			name:   "byte by byte",
			writes: bytesAsWrites(content),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := llm.NewBoundedBuffer(limit)
			for _, w := range tc.writes {
				b.Write(w)
			}
			if !b.Truncated() {
				t.Fatal("超限后 Truncated 应为 true")
			}
			got := b.Bytes()
			if len(got) > limit {
				t.Errorf("len(Bytes()) = %d, 超过上限 %d", len(got), limit)
			}
			if string(got) != want {
				t.Errorf("Bytes() = %q, want %q", got, want)
			}
		})
	}
}

// TestBoundedBufferRuneBoundaries 用 CJK、emoji 及混合内容验证截断切分
// 始终落在 rune 边界：输出是合法 UTF-8，且标记两侧分别是完整的头尾字符。
func TestBoundedBufferRuneBoundaries(t *testing.T) {
	const limit = 40
	cases := []struct {
		name     string
		content  string
		writes   [][]byte
		headRune string
		tailRune string
	}{
		{
			name: "cjk single write", content: strings.Repeat("中", 50),
			writes: [][]byte{[]byte(strings.Repeat("中", 50))}, headRune: "中", tailRune: "中",
		},
		{
			name: "emoji chunked", content: strings.Repeat("🚀", 50),
			writes:   [][]byte{[]byte(strings.Repeat("🚀", 20)), []byte(strings.Repeat("🚀", 30))},
			headRune: "🚀", tailRune: "🚀",
		},
		{
			name: "mixed", content: strings.Repeat("a中🚀", 40),
			writes: [][]byte{[]byte(strings.Repeat("a中🚀", 40))},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := llm.NewBoundedBuffer(limit)
			for _, w := range tc.writes {
				b.Write(w)
			}
			got := b.Bytes()
			if !utf8.Valid(got) {
				t.Fatalf("截断产生了非法 UTF-8: %q", got)
			}
			if len(got) > limit {
				t.Errorf("len(Bytes()) = %d, 超过上限 %d", len(got), limit)
			}
			s := string(got)
			if !strings.Contains(s, boundedMarker) {
				t.Errorf("输出缺少截断标记: %q", s)
			}
			// 标记两侧必须是完整 rune（以对应字符开头/结尾）。
			parts := strings.SplitN(s, boundedMarker, 2)
			if len(parts) != 2 {
				t.Fatalf("截断标记应恰好出现一次: %q", s)
			}
			if tc.headRune != "" && !strings.HasPrefix(parts[0], tc.headRune) {
				t.Errorf("头部不是以 %q 开头: %q", tc.headRune, parts[0])
			}
			if tc.tailRune != "" && !strings.HasSuffix(parts[1], tc.tailRune) {
				t.Errorf("尾部不是以 %q 结尾: %q", tc.tailRune, parts[1])
			}
		})
	}

	// 逐字节写入一个被拆散的多字节流，最终仍应是合法 UTF-8。
	t.Run("split runes byte by byte", func(t *testing.T) {
		b := llm.NewBoundedBuffer(limit)
		for _, w := range bytesAsWrites(strings.Repeat("中🚀", 30)) {
			b.Write(w)
		}
		if got := b.Bytes(); !utf8.Valid(got) {
			t.Fatalf("逐字节写入产生非法 UTF-8: %q", got)
		}
	})
}

// TestBoundedBufferLimitBoundary 围绕上限取值验证：仅当写入量严格超过
// 上限时才截断，且任意上限下输出长度都不超过上限（含极小上限）。
func TestBoundedBufferLimitBoundary(t *testing.T) {
	content := strings.Repeat("z", 100)
	cases := []struct {
		name      string
		limit     int
		wantTrunc bool
	}{
		{name: "one over limit", limit: 99, wantTrunc: true},
		{name: "exact limit", limit: 100, wantTrunc: false},
		{name: "under limit", limit: 101, wantTrunc: false},
		{name: "tiny limit 8", limit: 8, wantTrunc: true},
		{name: "tiny limit 1", limit: 1, wantTrunc: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := llm.NewBoundedBuffer(tc.limit)
			b.Write([]byte(content))
			got := b.Bytes()
			if len(got) > tc.limit {
				t.Errorf("len(Bytes()) = %d, 超过上限 %d", len(got), tc.limit)
			}
			if b.Truncated() != tc.wantTrunc {
				t.Errorf("Truncated() = %v, want %v", b.Truncated(), tc.wantTrunc)
			}
			if tc.wantTrunc && len(boundedMarker) > tc.limit && string(got) != boundedMarker[:tc.limit] {
				t.Errorf("极小上限输出 = %q, want 截断标记前 %d 字节", got, tc.limit)
			}
		})
	}
}

// TestBoundedBufferReset 验证 Reset 清空内容、退出截断态，并可再次使用；
// 同时验证 Bytes 返回的是副本。
func TestBoundedBufferReset(t *testing.T) {
	b := llm.NewBoundedBuffer(40)
	b.Write([]byte(strings.Repeat("q", 100)))
	if !b.Truncated() || !strings.Contains(string(b.Bytes()), boundedMarker) {
		t.Fatal("首次写入应进入截断态")
	}

	snap := b.Bytes()
	for i := range snap {
		snap[i] = 'X'
	}
	if strings.Contains(string(b.Bytes()), "X") {
		t.Fatal("修改 Bytes() 返回值影响了内部状态")
	}

	b.Reset()
	if b.Truncated() {
		t.Fatal("Reset 后 Truncated 应为 false")
	}
	if got := b.Bytes(); len(got) != 0 {
		t.Errorf("Reset 后 Bytes() = %q, want 空", got)
	}

	b.Write([]byte("fresh"))
	if b.Truncated() || string(b.Bytes()) != "fresh" {
		t.Errorf("Reset 后重新写入 = %q, truncated=%v", b.Bytes(), b.Truncated())
	}
	b.Write([]byte(strings.Repeat("w", 100)))
	if !b.Truncated() || len(b.Bytes()) > 40 {
		t.Errorf("Reset 后再次超限行为异常: %q", b.Bytes())
	}
}

// TestBoundedBufferDefaultLimit 覆盖 limit<=0 采用默认上限，以及零值
// BoundedBuffer 可直接使用的约定。
func TestBoundedBufferDefaultLimit(t *testing.T) {
	const defaultLimit = 4096
	big := strings.Repeat("m", defaultLimit+100)

	cases := []struct {
		name string
		b    *llm.BoundedBuffer
	}{
		{name: "zero limit", b: llm.NewBoundedBuffer(0)},
		{name: "negative limit", b: llm.NewBoundedBuffer(-7)},
		{name: "zero value", b: &llm.BoundedBuffer{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.b.Write([]byte(big))
			got := tc.b.Bytes()
			if !tc.b.Truncated() {
				t.Error("超过默认上限应截断")
			}
			if len(got) == 0 || len(got) > defaultLimit {
				t.Errorf("len(Bytes()) = %d, want (0, %d]", len(got), defaultLimit)
			}
		})
	}
}

// TestBoundedBufferConcurrent 在多个 goroutine 交错执行 Write、Bytes 与
// Truncated，验证锁保护下既不越界也不产生数据竞争（配合 -race 运行）。
func TestBoundedBufferConcurrent(t *testing.T) {
	const limit = 128
	b := llm.NewBoundedBuffer(limit)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.Write([]byte("写入payload🚀"))
			}
		}()
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				got := b.Bytes()
				if len(got) > limit {
					t.Errorf("并发读取到超限输出: %d", len(got))
					return
				}
				if !utf8.Valid(got) && len(got) > 0 {
					t.Errorf("并发读取到非法 UTF-8: %q", got)
					return
				}
				_ = b.Truncated()
			}
		}()
	}
	wg.Wait()

	got := b.Bytes()
	if !b.Truncated() {
		t.Error("大量写入后应处于截断态")
	}
	if len(got) == 0 || len(got) > limit {
		t.Errorf("最终 len(Bytes()) = %d, want (0, %d]", len(got), limit)
	}
	if !utf8.Valid(got) {
		t.Errorf("最终输出非法 UTF-8: %q", got)
	}
}

// bytesAsWrites 把字符串拆成逐字节的写入序列，便于测试跨 Write 的边界。
func bytesAsWrites(s string) [][]byte {
	out := make([][]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		out = append(out, []byte{s[i]})
	}
	return out
}
