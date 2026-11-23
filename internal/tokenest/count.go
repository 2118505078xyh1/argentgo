// Package tokenest 提供 token 成本的批量估算。
//
// 与 internal/tokenize 的关系：tokenize 保存单条文本的既有语义（ASCII
// ceil(len/4)、宽 rune 各 1 token），本包不修改也不取代它；本包只在"批量"
// 入口使用——先把一批字符串拷进单个 []byte、用 span 记录边界，再按阈值
// 决定是否并行计数。启发式在 ASCII/宽 rune 上与 tokenize 对齐，另对数字串
// 与标点做分档高估（对预算而言高估是安全方向，见 tokenize 包注释）。
//
// 不嵌入任何第三方词表：这是预算用的廉价估算，不是请求计价或精确 tokenizer。
package tokenest

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

// parallelBatchMin 是并行计数的条目阈值：小于它时 fan-out 的固定开销不划算。
const parallelBatchMin = 16

// Item 是一个可估算条目，Kind 用于聚合（如消息角色、工具调用）。
type Item struct {
	Kind string
	Text string
}

type span struct {
	start, end int
}

// CountBatch 批量估算一组字符串的 token 成本，返回与入参同下标的计数。
//
// 所有文本先拷入单个 []byte（一次分配），span 切片记录每条的边界；随后
// 仅在条目数 >= parallelBatchMin 且 CPU>1 时 fan-out 到固定 worker 池，
// 否则串行。每个 worker 只写自己的 out[i]，无需锁。
func CountBatch(texts []string) []int {
	out := make([]int, len(texts))
	if len(texts) == 0 {
		return out
	}

	total := 0
	for _, s := range texts {
		total += len(s)
	}
	buf := make([]byte, total)
	spans := make([]span, len(texts))
	off := 0
	for i, s := range texts {
		copy(buf[off:], s)
		spans[i] = span{start: off, end: off + len(s)}
		off += len(s)
	}

	procs := runtime.GOMAXPROCS(0)
	if len(texts) >= parallelBatchMin && procs > 1 {
		// 每个 worker 至少分到 32 条才值得起 goroutine；worker 数再受 CPU 数封顶。
		// 用原子游标分发而非 channel：每次分发只有一次原子加，避免 channel 的
		// 缓冲区分配与收发开销吞噬并行收益。
		workers := procs
		if workers > 16 {
			workers = 16
		}
		if workers > (len(texts)+31)/32 {
			workers = (len(texts) + 31) / 32
		}
		var next atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for {
					i := int(next.Add(1)) - 1
					if i >= len(texts) {
						return
					}
					sp := spans[i]
					out[i] = countSpan(buf[sp.start:sp.end])
				}
			}()
		}
		wg.Wait()
		return out
	}

	for i, sp := range spans {
		out[i] = countSpan(buf[sp.start:sp.end])
	}
	return out
}

// countSpan 估算单段文本的 token 成本。
//
//   - 空串为 0；纯 ASCII 直接 ceil(len/4)，与 tokenize.Estimate 完全一致；
//   - 混合文本按 rune 分档：宽 rune（CJK/全角/emoji）各 1；字母 4:1；
//     数字串 3:1；ASCII 标点 2:1；其余窄字符 4:1。各档独立向上取整。
func countSpan(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return (len(b) + tokenize.CharsPerToken - 1) / tokenize.CharsPerToken
	}

	var wide, letters, digits, punct, other int
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		switch {
		case tokenize.IsWide(r):
			wide++
		case r >= '0' && r <= '9':
			digits++
		case isASCIILetter(r):
			letters++
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			other++
		case r < 0x80: // ASCII 标点
			punct++
		default: // 其他窄字符（拉丁扩展等），按字母档处理
			other++
		}
	}
	return wide +
		(letters+3)/4 +
		(digits+2)/3 +
		(punct+1)/2 +
		(other+3)/4
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}

// EstimateItems 先批量计数，再按 Kind 聚合，返回总和与分 Kind 明细。
// 所有累加都走饱和加法，极端输入也不会溢出成负数。
func EstimateItems(items []Item) (total int, byKind map[string]int) {
	texts := make([]string, len(items))
	for i := range items {
		texts[i] = items[i].Text
	}
	counts := CountBatch(texts)
	byKind = make(map[string]int, 4)
	for i, c := range counts {
		total = satAddInt(total, c)
		byKind[items[i].Kind] = satAddInt(byKind[items[i].Kind], c)
	}
	return total, byKind
}

// satAddInt 对 int 做饱和加法，避免累计溢出。
func satAddInt(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	if b < 0 && a < math.MinInt-b {
		return math.MinInt
	}
	return a + b
}
