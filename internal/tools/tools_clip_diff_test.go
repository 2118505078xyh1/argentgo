package tools

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// 本文件为 tools_clip.go 中无状态的 UTF-8 截断辅助函数
// 添加差分/属性测试。它复用 tools_clip_utf8_test.go（同包）中的
// clipMarker、splitClipped 和
// assertClipInvariants，从不触碰生产代码。

// reassembleChunks 从给定大小的连续字节块重建文本。
// 非正的 size 表示"整个输入"（0 字节切分是空操作）。
// 按原始字节切分会在 size 为 1/2/3 时有意切断多字节 rune；
// 重组必须逐字节还原输入。
func reassembleChunks(text string, chunkSize int) string {
	if chunkSize <= 0 {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i += chunkSize {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		b.WriteString(text[i:end])
	}
	return b.String()
}

// TestClipToDeterministicAcrossInputChunking 固定了"任意分块
// 等价"这一属性。clipTo 是无状态的纯一次性函数，因此
// 无论字节如何分区，喂给它相同的字节都必须产生
// 相同的结果。这可以防止未来任何让输出依赖
// 增量/分块对齐状态的重构。
func TestClipToDeterministicAcrossInputChunking(t *testing.T) {
	texts := []struct {
		name string
		text string
	}{
		{"ascii", "abcdefghijklmnopqrstuvwxyz0123456789"},
		{"chinese", strings.Repeat("中文裁剪测试工具", 12)},
		{"emoji", strings.Repeat("😀🚀🎉", 10)},
		{"combining marks", strings.Repeat("e\u0301a\u0301", 12)},
		{"mixed scripts", strings.Repeat("ab中😀c\u0301", 9)},
	}
	limits := []int{1, 2, 7, 13, 25, 60, 200}
	// 0 表示"整个输入"的分块（下面的基准）。
	chunkSizes := []int{0, 1, 2, 3, 7, 16}

	for _, tc := range texts {
		for _, limit := range limits {
			whole := clipTo(tc.text, limit)
			for _, size := range chunkSizes {
				t.Run(fmt.Sprintf("%s/limit=%d/chunk=%d", tc.name, limit, size), func(t *testing.T) {
					reassembled := reassembleChunks(tc.text, size)
					if reassembled != tc.text {
						t.Fatalf("reassembly changed the input: got %q, want %q", reassembled, tc.text)
					}
					if got := clipTo(reassembled, limit); got != whole {
						t.Fatalf("clipTo on %d-byte chunks differs from the whole-input call:\n got: %q\nwant: %q", size, got, whole)
					}
				})
			}
		}
	}
}

// runeEnds 返回 text 中所有 rune 边界（含 0 与 len）。它按原始
// 字节解码，非法字节每个占 1 字节，因此参考实现不会像 WriteRune
// 那样把坏字节替换成 U+FFFD。
func runeEnds(text string) []int {
	ends := make([]int, 0, len(text)+1)
	ends = append(ends, 0)
	for i := 0; i < len(text); {
		_, size := utf8.DecodeRuneInString(text[i:])
		if size <= 0 {
			size = 1
		}
		i += size
		ends = append(ends, i)
	}
	return ends
}

// splitBudgetClip 按给定的边界列表选取头尾：头部是能装入
// headBudget 字节的最大前缀，尾部是能装入 tailBudget 字节的最大
// 后缀。naiveClip 与 graphemeClip 共用它，只替换边界的来源。
func splitBudgetClip(text string, limit int, ends []int) (head, tail string, truncated bool) {
	budget := limit
	if budget <= 0 {
		budget = maxToolOutput // clipTo treats non-positive limits as the default.
	}
	if len(text) <= budget {
		return text, "", false
	}
	headBudget, tailBudget := budget*2/3, budget-budget*2/3

	hEnd := 0
	for i := 1; i < len(ends); i++ {
		if ends[i] > headBudget {
			break
		}
		hEnd = ends[i]
	}
	used := 0
	tStart := len(text)
	for i := len(ends) - 1; i > 0; i-- {
		size := ends[i] - ends[i-1]
		if used+size > tailBudget {
			break
		}
		used += size
		tStart = ends[i-1]
	}
	return text[:hEnd], text[tStart:], true
}

// refClusterEnd 返回从 at 开始的字素簇末端。它从开头逐簇扫描，
// 使用与生产代码相同的 Glues 规则，但实现路径独立于
// ClusterBoundaryBefore/After，用于差分验证。
func refClusterEnd(text string, at int) int {
	prevR, size := textutil.DecodeRuneAt(text, at)
	if size <= 0 {
		return at
	}
	prev := textutil.ClusterRune(prevR, size)
	j := at + size
	for j < len(text) {
		currR, currSize := textutil.DecodeRuneAt(text, j)
		if currSize <= 0 {
			break
		}
		curr := textutil.ClusterRune(currR, currSize)
		if !textutil.Glues(prev, curr) {
			break
		}
		j += currSize
		prev = curr
	}
	return j
}

// graphemeEnds 返回 text 中所有字素簇边界（含 0 与 len）。
func graphemeEnds(text string) []int {
	ends := make([]int, 0, len(text)+1)
	ends = append(ends, 0)
	for i := 0; i < len(text); {
		next := refClusterEnd(text, i)
		if next <= i {
			next = i + 1
		}
		ends = append(ends, next)
		i = next
	}
	return ends
}

// graphemeClip 是 clipTo 的字素粒度参考实现：头尾都落在字素簇
// 边界上，而不仅是 rune 边界。它与 naiveClip 共用预算选择逻辑，
// 只把边界来源换成 graphemeEnds。
func graphemeClip(text string, limit int) (head, tail string, truncated bool) {
	return splitBudgetClip(text, limit, graphemeEnds(text))
}

// naiveClip 是按 rune 粒度的旧参考实现，仅在单 rune 字素样本上
// 与 clipTo 逐字节一致；对组合序列/ZWJ/旗帜等多 rune 字簇，
// 必须使用 graphemeClip。保留它用于回归对比。
func naiveClip(text string, limit int) (head, tail string, truncated bool) {
	return splitBudgetClip(text, limit, runeEnds(text))
}

// clipAtoms 是用于构造随机 UTF-8 文本的音节：ASCII、中文、
// 四字节 emoji 和组合标记（包括独立的标记）。
var clipAtoms = []string{
	"a", "b", "z", "0", "9", " ", "_",
	"中", "文", "测", "试", "工", "具",
	"😀", "🚀", "🎉", "👍",
	"e\u0301", "a\u0301", "\u0301",
}

func randomClipText(rng *rand.Rand, maxAtoms int) string {
	n := rng.Intn(maxAtoms) + 1
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(clipAtoms[rng.Intn(len(clipAtoms))])
	}
	return b.String()
}

// randomClipLimit 有意混合小限制、接近文本长度的
// 限制、非正限制（默认预算）以及完全跳过
// 截断的限制，使截断和未改变两个分支都被覆盖。
func randomClipLimit(rng *rand.Rand, textLen int) int {
	switch rng.Intn(5) {
	case 0:
		return rng.Intn(textLen + 1)
	case 1:
		return rng.Intn(24) + 1
	case 2:
		return textLen/2 + rng.Intn(10)
	case 3:
		return rng.Intn(10) - 5 // negative .. 4
	default:
		return textLen + rng.Intn(20) // no truncation expected
	}
}

// TestClipToMatchesRuneReferenceOnRandomText 是固定种子的差分测试：
// 在随机的混合文字 UTF-8 文本上把 clipTo 与 graphemeClip 比较
// 以及限制值。除了共享的不变量（有效 UTF-8、rune/字节预算、
// 恒定的标记开销），保留的头尾内容还必须与
// 按字素簇粒度的参考逐字节一致。历史上这里用按 rune 粒度的
// naiveClip，会把 e\u0301 等字簇切在中间，现已同步升级。
func TestClipToMatchesRuneReferenceOnRandomText(t *testing.T) {
	const seed = 20261120
	const iterations = 5000
	rng := rand.New(rand.NewSource(seed))

	for i := 0; i < iterations; i++ {
		text := randomClipText(rng, 120)
		limit := randomClipLimit(rng, len(text))
		got := clipTo(text, limit)

		assertClipInvariants(t, text, limit, got)

		refHead, refTail, refTrunc := graphemeClip(text, limit)
		head, tail, trunc := splitClipped(t, got)
		if trunc != refTrunc {
			t.Fatalf("iteration %d: truncation mismatch: got %v, reference %v (limit=%d, text=%q, got=%q)",
				i, trunc, refTrunc, limit, text, got)
		}
		if !trunc {
			if got != text {
				t.Fatalf("iteration %d: untruncated result changed the text (limit=%d): got %q, want %q",
					i, limit, got, text)
			}
			continue
		}
		if head != refHead || tail != refTail {
			t.Fatalf("iteration %d: retained content differs from grapheme reference (limit=%d):\n head got:  %q\n head want: %q\n tail got:  %q\n tail want: %q\n text: %q",
				i, limit, head, refHead, tail, refTail, text)
		}
	}
}

// TestClipToDefaultLimitMatchesRuneReference 覆盖非正限制
// 分支，使用足够大、确实超过 maxToolOutput 的文本，而
// 短的随机文本无法达到该大小。参考同样是字素粒度的
// graphemeClip（文本包含 b\u0301 这样的多 rune 字簇）。
func TestClipToDefaultLimitMatchesRuneReference(t *testing.T) {
	text := strings.Repeat("😀中ab\u0301", 6000) // well over maxToolOutput bytes
	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			got := clipTo(text, limit)
			assertClipInvariants(t, text, limit, got)

			refHead, refTail, refTrunc := graphemeClip(text, limit)
			head, tail, trunc := splitClipped(t, got)
			if !trunc || !refTrunc {
				t.Fatalf("clipTo(..., %d) on a %d-byte text should truncate", limit, len(text))
			}
			if head != refHead || tail != refTail {
				t.Fatalf("clipTo(..., %d) content differs from grapheme reference:\n head got:  %q\n head want: %q\n tail got:  %q\n tail want: %q",
					limit, head, refHead, tail, refTail)
			}
		})
	}
}

// benchClipResult 让基准结果对编译器保持可观察。
var benchClipResult string

// BenchmarkClipToLargeInput 记录 clipTo 在大型
// ASCII、中文、emoji 和混合输入上的纯性能基线。它有意不设置
// 任何墙上时钟阈值；它存在的意义只是让回归能通过
// 比较连续几次运行而被发现。
func BenchmarkClipToLargeInput(b *testing.B) {
	cases := []struct {
		name  string
		text  string
		limit int
	}{
		{"ascii-1KB", strings.Repeat("abcdefghij", 100), 500},
		{"chinese-150KB", strings.Repeat("中文裁剪测试工具", 6250), 20000},
		{"emoji-200KB", strings.Repeat("😀🚀", 25000), 30000},
		{"mixed-180KB", strings.Repeat("ab中😀e\u0301", 15000), 40000},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchClipResult = clipTo(bc.text, bc.limit)
			}
		})
	}
}
