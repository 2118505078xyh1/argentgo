package agent

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// 本文件提供"重复踩同一个坑"检测的纯原语：把错误文本规范化为稳定签名，
// 再用并发安全的计数器统计每个签名出现的次数。刻意不接入实际错误路径——
// 接线由装配层统一完成，避免多处改动产生冲突（与 loop_detection.go 同一约定）。

// volatileToken 是所有易变片段（UUID、时间戳、数字、路径、十六进制串）替换后的统一占位符。
// 用同一个占位符可以让"仅易变片段不同"的错误最大程度地折叠到同一签名。
const volatileToken = "<*>"

// maxSignatureRunes 限制签名的最大长度（按 rune 计），避免超长错误信息占用过多内存；
// 按 rune 截断以保全 CJK 与 emoji，不会切出半个多字节字符。
const maxSignatureRunes = 512

// defaultPatternTopLimit 是 Top 未指定显式上限（<=0）时返回的最大条目数。
const defaultPatternTopLimit = 10

// 规范化用的正则在包初始化时编译一次，避免每次调用重复编译。
//
// 顺序很关键：UUID 与 ISO8601 时间戳先于十六进制串和数字剥离（它们本身由
// 十六进制/数字组成，若不先整体替换会被拆碎）；绝对路径先于数字剥离，
// 使整条路径折叠成一个占位符而不是留下若干数字碎片。
var (
	uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

	// ISO8601：日期(YYYY-MM-DD)，可带 T/空格分隔的时间、小数秒与 Z/±HH:MM 时区。
	// 注意本正则作用于已转小写的文本，因此 T/Z 须同时接受小写形式。
	iso8601Pattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:[Tt ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:[Zz]|[+-]\d{2}:?\d{2})?)?`)

	// 十六进制：0x 前缀的串，或至少 8 位连续十六进制数字。
	// 设置 8 位门槛是为了不误伤 "dead"/"cafe"/"face" 之类的普通英文单词。
	hexPattern = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b|\b[0-9a-f]{8,}\b`)

	// Windows 绝对路径：盘符 + 反斜杠或正斜杠（覆盖 C:\foo 与 C:/foo 两种写法）。
	winPathPattern = regexp.MustCompile(`[a-zA-Z]:[\\/](?:[^\\/\s]+[\\/])*[^\\/\s]*`)

	// Unix 绝对路径：至少两段（/a/b）。要求两段是为了不误伤 "and/or"、"N/A" 等文本。
	unixPathPattern = regexp.MustCompile(`/(?:[^/\s]+/)+[^/\s]+`)

	// 独立数字（Go 的 \d 为 ASCII 数字，不会触碰 CJK 数字）。
	numberPattern = regexp.MustCompile(`\d+`)

	// 空白折叠。
	whitespacePattern = regexp.MustCompile(`\s+`)
)

// normalizeErrorText 把错误文本归一化为规范文本：统一小写，剥离易变片段，
// 折叠空白。CJK/emoji 不受影响——所有正则都只匹配 ASCII 结构，strings.ToLower
// 对多字节字符是恒等操作，因此 CJK 内容原样保留。
func normalizeErrorText(err string) string {
	text := strings.ToLower(err)
	text = uuidPattern.ReplaceAllString(text, volatileToken)
	text = iso8601Pattern.ReplaceAllString(text, volatileToken)
	text = hexPattern.ReplaceAllString(text, volatileToken)
	text = winPathPattern.ReplaceAllString(text, volatileToken)
	text = unixPathPattern.ReplaceAllString(text, volatileToken)
	text = numberPattern.ReplaceAllString(text, volatileToken)
	text = whitespacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// NormalizeErrorSignature 返回错误文本的稳定签名：即规范化后的文本（小写、
// 易变片段折叠为占位符、空白折叠），必要时按 rune 截断到 maxSignatureRunes。
//
// 语义要点：
//   - 仅易变片段（UUID/ISO8601 时间戳/数字/绝对路径/十六进制串）不同的错误
//     会得到相同签名，从而能被识别为"同一个坑"。
//   - 真正不同的错误文本得到不同签名。
//   - CJK 与 emoji 原样保留，不会被当作易变内容剥离或切断。
//   - 纯函数：不读写全局状态、不修改入参、不读时钟、不做 I/O，可任意并发调用。
func NormalizeErrorSignature(err string) string {
	text := normalizeErrorText(err)
	runes := []rune(text)
	if len(runes) > maxSignatureRunes {
		return string(runes[:maxSignatureRunes])
	}
	return text
}

// ErrorPattern 是 Top 返回的一个高频错误签名及其出现次数。
type ErrorPattern struct {
	Signature string
	Count     int
}

// ErrorPatternTracker 按签名统计错误出现次数，可安全并发使用，零值即可用。
//
// window 为滑动窗口大小（仅统计最近 window 条记录）；<=0 表示不淘汰，即累计计数。
// limit 为 Top 返回的最大条目数；<=0 取 defaultPatternTopLimit。两者都通过
// NewErrorPatternTracker 配置，零值使用默认语义（累计计数 + 默认 Top 上限）。
type ErrorPatternTracker struct {
	mu     sync.Mutex
	window int
	limit  int
	seq    uint64         // 递增序号，用于 Top 的稳定排序（首次出现先后）
	recent []string       // 窗口内的签名，按记录顺序排列
	counts map[string]int // 签名 -> 窗口内出现次数
	first  map[string]uint64
}

// NewErrorPatternTracker 用给定的窗口与 Top 上限构建追踪器。
// window<=0 表示不淘汰（累计计数）；limit<=0 使用默认 Top 上限。
func NewErrorPatternTracker(window, limit int) *ErrorPatternTracker {
	return &ErrorPatternTracker{window: window, limit: limit}
}

// Record 记录一次错误，返回该错误签名在当前窗口内的累计出现次数（含本次）。
// nil 接收方安全地返回 0。Record 可被多个 goroutine 并发调用。
func (t *ErrorPatternTracker) Record(err string) int {
	if t == nil {
		return 0
	}
	sig := NormalizeErrorSignature(err)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.counts == nil {
		t.counts = make(map[string]int)
	}
	if t.first == nil {
		t.first = make(map[string]uint64)
	}

	t.seq++
	if _, ok := t.first[sig]; !ok {
		t.first[sig] = t.seq
	}
	t.recent = append(t.recent, sig)
	t.counts[sig]++

	// 淘汰超出窗口的最旧记录，保持计数与窗口一致。
	if t.window > 0 && len(t.recent) > t.window {
		n := len(t.recent) - t.window
		for _, old := range t.recent[:n] {
			t.counts[old]--
			if t.counts[old] <= 0 {
				delete(t.counts, old)
				delete(t.first, old)
			}
		}
		copy(t.recent, t.recent[n:])
		t.recent = t.recent[:t.window]
	}

	return t.counts[sig]
}

// Top 按出现次数降序列出高频签名，最多返回 limit 条（零值追踪器用默认上限）。
// 并列时先出现的签名排在前面，其次按签名文本升序，因此排序确定、重复调用结果稳定。
// 返回的是新切片，调用方修改它不会影响追踪器内部状态。
func (t *ErrorPatternTracker) Top() []ErrorPattern {
	if t == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]ErrorPattern, 0, len(t.counts))
	for sig, count := range t.counts {
		out = append(out, ErrorPattern{Signature: sig, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		fi, fj := t.first[out[i].Signature], t.first[out[j].Signature]
		if fi != fj {
			return fi < fj
		}
		return out[i].Signature < out[j].Signature
	})

	limit := t.limit
	if limit <= 0 {
		limit = defaultPatternTopLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Reset 清空所有已记录的签名与计数，但保留窗口与上限配置。
func (t *ErrorPatternTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recent = nil
	t.counts = nil
	t.first = nil
	t.seq = 0
}
