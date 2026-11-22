// Package predict 提供纯 Go 的 n-gram 前缀补全原语。
//
// 核心思想对标 omp（oh-my-pi）的 crates/predict n-gram 引擎：从代码文本统计
// n-gram 转移分布，用绝对折扣插值（高阶上下文 → 低阶上下文 → unigram）在
// 给定前缀下对后继 token 排序。不引入外部模型、GGUF、正则库或任何第三方依赖。
package predict

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// 默认保留的上下文条目上限（含 unigram）。
	defaultMaxEntries = 10000
	// 最大阶数：限制上下文键长度，避免病态输入导致超长字符串键。
	maxOrder = 8
	// Predict 接受的前缀字节上限，超出时只保留尾部（光标附近最有信息量）。
	maxPrefixBytes = 4096
	// 单次 Predict 返回的建议数上限。
	maxSuggestions = 256
	// 上下文键各 token 之间的分隔符；tokenizer 永不产出 NUL，故连接/拆分无歧义。
	separator = "\x00"
	// 绝对折扣系数与低阶回退的平滑下限。
	defaultDiscount = 0.5
	defaultFloor    = 0.1
)

// Suggestion 是一条补全建议。
// Token 是完整的后继 token（不是相对前缀的后缀）；Score 为归一化概率估计，越大越好。
type Suggestion struct {
	Token string
	Score float64
}

// contextEntry 是单个上下文（长度为 order-1 的 token 序列）的转移分布。
type contextEntry struct {
	followers map[string]int // 后继 token -> 出现次数
	total     int            // 所有后继次数之和
	lastUsed  uint64         // 最近一次被训练更新的时钟，用于容量淘汰（LRU 近似）
}

// Model 是一个 n-gram 前缀补全模型。
//
// 并发语义：Train/Learn 为写操作，Predict 为纯读操作。内部用 sync.RWMutex 保护，
// 多个 Predict 可并发执行，Train 会独占写锁；Predict 不修改任何共享状态。
// 零值不可用，请用 New 构造。
type Model struct {
	mu         sync.RWMutex
	n          int
	maxEntries int
	discount   float64
	floor      float64
	clock      uint64
	contexts   map[string]*contextEntry // key 为上下文 token 用 NUL 连接；"" 表示 unigram
}

// New 构造一个 n 阶模型。n = 上下文长度 + 1（例如 3 表示 trigram：前两个 token 预测第三个）。
// n < 1 时按 1 处理；n > 8 时截断到 8。
func New(n int) *Model {
	if n < 1 {
		n = 1
	}
	if n > maxOrder {
		n = maxOrder
	}
	return &Model{
		n:          n,
		maxEntries: defaultMaxEntries,
		discount:   defaultDiscount,
		floor:      defaultFloor,
		contexts:   make(map[string]*contextEntry),
	}
}

// Order 返回模型阶数。
func (m *Model) Order() int { return m.n }

// SetMaxEntries 设置模型保留的上下文条目上限（含 unigram）。默认 10000。
// unigram 条目（key=""）永不被淘汰；超限时按 lastUsed 最小者淘汰。
func (m *Model) SetMaxEntries(max int) {
	if max < 1 {
		max = 1
	}
	m.mu.Lock()
	m.maxEntries = max
	m.trimLocked()
	m.mu.Unlock()
}

// Train 从文本学习 n-gram 转移；等价于 Learn。
func (m *Model) Train(text string) { m.Learn(text) }

// Learn 从文本学习 n-gram 转移。每个位置的 token 会依次更新 1..n 阶分布：
// unigram、bigram、trigram …… 直到长度 n-1 的上下文。
func (m *Model) Learn(text string) {
	toks := tokenize(text)
	if len(toks) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, tok := range toks {
		upper := min(m.n, i+1)
		for k := 1; k <= upper; k++ {
			key := joinTokens(toks[i-k+1 : i])
			e := m.contexts[key]
			if e == nil {
				e = &contextEntry{followers: make(map[string]int)}
				m.contexts[key] = e
			}
			e.followers[tok]++
			e.total++
			e.lastUsed = m.clock
			m.clock++
		}
	}
	m.trimLocked()
}

// Predict 给定前缀，返回最可能的后继 token。
//
// 语义：prefix 是光标前的原始文本。取末尾 token 作为可能正在输入的“部分 token”
// （仅当它是标识符时），其前面的 token 作为上下文；若末尾是标点/空白，则部分 token
// 为空、全部 token 都作为上下文。候选来自各阶上下文的后继并做前缀过滤，按分数降序、
// 同分按 token 字典序（保证确定性），最多返回 limit 条。limit <= 0 返回 nil。
func (m *Model) Predict(prefix string, limit int) []Suggestion {
	if limit <= 0 {
		return nil
	}
	if limit > maxSuggestions {
		limit = maxSuggestions
	}

	toks := tokenize(boundPrefix(prefix))
	partial := ""
	context := toks
	if len(toks) > 0 && isIdentToken(toks[len(toks)-1]) {
		partial = strings.ToLower(toks[len(toks)-1])
		context = toks[:len(toks)-1]
	}

	// 只使用最近 n-1 个上下文 token；m = 上下文长度，对应的链从 order=m+1 开始。
	mLen := min(m.n-1, len(context))
	key := joinTokens(context[len(context)-mLen:])

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.contexts) == 0 {
		return nil
	}

	// 收集候选：从最高阶上下文逐级回退到 unigram 的所有后继。
	seen := make(map[string]struct{})
	var cands []string
	for k := key; ; k = parentKey(k) {
		if e := m.contexts[k]; e != nil {
			for tok := range e.followers {
				if _, ok := seen[tok]; ok {
					continue
				}
				seen[tok] = struct{}{}
				if partial == "" || strings.HasPrefix(strings.ToLower(tok), partial) {
					cands = append(cands, tok)
				}
			}
		}
		if k == "" {
			break
		}
	}

	out := make([]Suggestion, 0, len(cands))
	for _, tok := range cands {
		out = append(out, Suggestion{Token: tok, Score: m.prob(mLen+1, key, tok)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Token < out[j].Token
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// prob 计算绝对折扣插值概率 P(tok | key)，其中 order = 上下文长度 + 1。
// P_1(w) = (c(w) + floor) / (C + floor*(V+1))，提供平滑下限避免零概率；
// P_k(w) = (max(c(w)-d, 0) + (d*T + floor)*P_{k-1}(w)) / (C + floor)，
// 其中 C 为上下文总次数、T 为不同后继数。低阶上下文缺失时直接回退到 unigram。
func (m *Model) prob(order int, key, tok string) float64 {
	e := m.contexts[key]
	if e == nil {
		return 0
	}
	c := float64(e.followers[tok])
	if order <= 1 || key == "" {
		return (c + m.floor) / (float64(e.total) + m.floor*float64(len(e.followers)+1))
	}
	pk := parentKey(key)
	lower := m.prob(order-1, pk, tok)
	if lower == 0 {
		lower = m.prob(1, "", tok)
	}
	if e.total == 0 {
		return lower
	}
	t := float64(len(e.followers))
	discounted := math.Max(c-m.discount, 0)
	return (discounted + (m.discount*t+m.floor)*lower) / (float64(e.total) + m.floor)
}

// trimLocked 在超出上限时淘汰 lastUsed 最小的上下文条目；调用方须持写锁。
// unigram 条目（key=""）永不淘汰，因此至少保留一个条目。
func (m *Model) trimLocked() {
	for len(m.contexts) > m.maxEntries {
		var victim string
		var oldest uint64 = math.MaxUint64
		for key, e := range m.contexts {
			if key == "" {
				continue
			}
			if e.lastUsed < oldest {
				oldest = e.lastUsed
				victim = key
			}
		}
		if victim == "" {
			return
		}
		delete(m.contexts, victim)
	}
}

// tokenize 把代码文本切成 token：连续标识符字符、连续标点字符、连续空白各自成串。
// NUL 仅作内部键分隔符，Tokenizer 直接跳过它，保证 token 中不出现分隔符。
func tokenize(s string) []string {
	runes := []rune(s)
	classOf := func(r rune) int {
		switch {
		case r == 0:
			return -1
		case unicode.IsSpace(r):
			return 1
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			return 2
		default:
			return 3
		}
	}
	var out []string
	for i := 0; i < len(runes); {
		c := classOf(runes[i])
		if c == -1 {
			i++
			continue
		}
		start := i
		for i < len(runes) && classOf(runes[i]) == c {
			i++
		}
		out = append(out, string(runes[start:i]))
	}
	return out
}

// isIdentToken 报告 tok 是否为标识符 token（首字符为字母/数字/下划线）。
func isIdentToken(tok string) bool {
	if tok == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(tok)
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// joinTokens 用 NUL 连接 token；空切片返回空串（即 unigram 上下文键）。
func joinTokens(toks []string) string {
	if len(toks) == 0 {
		return ""
	}
	return strings.Join(toks, separator)
}

// parentKey 去掉上下文键的第一个 token，得到低一阶的回退键。
func parentKey(key string) string {
	if i := strings.Index(key, separator); i >= 0 {
		return key[i+len(separator):]
	}
	return ""
}

// boundPrefix 把过长前缀截断到尾部 maxPrefixBytes 字节，并在 rune 边界对齐。
func boundPrefix(s string) string {
	if len(s) <= maxPrefixBytes {
		return s
	}
	start := len(s) - maxPrefixBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
