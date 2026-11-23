package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

func clip(ctx context.Context, text string) string {
	// 仅限 opt-in：当 ctx 中没有溢出配置（也没有 env 开关）时，
	// 这正是旧的 clipTo 路径，因此所有现有调用方都不受影响。
	if out, handled := spillToolOutput(ctx, text); handled {
		return out
	}
	limit := outputLimit(ctx)

	// #33：超大结果存进会话内易失缓存，正文换成可回读的 argent:// URI。
	// 阈值远高于 maxToolOutput，常规工具输出仍走下面的裁剪帧。
	if out, handled := cacheOversizedToolResult(ctx, text); handled {
		return out
	}

	// #37：json.Marshal 探测 + 按膨胀比收缩预算，最多 4 轮。
	return clipWithJSONBudget(text, limit)
}

// clipTo 保留过大输出的头部和尾部。
//
// 注意：本函数刻意保持旧的字节精确帧（含 "\n... [truncated] ...\n"
// 标记），因为多处测试与调用方依赖该标记的固定长度。带 token/行数
// 损失头的默认路径由 clipWithLossHeader 提供，clip() 默认走那条路径。
func clipTo(text string, limit int) string {
	if limit <= 0 {
		limit = maxToolOutput
	}
	if len(text) <= limit {
		return text
	}
	head := limit * 2 / 3
	tail := limit - head
	// 头部只回退到最近的字素簇边界，避免切断组合序列/ZWJ emoji/
	// 旗帜等；textutil.ClusterBoundaryBefore 内部复用 TrimPartialRune，
	// 因此仍保证 rune 对齐。
	head = textutil.ClusterBoundaryBefore(text, head)
	// 尾部切片从 len(text)-tail 开始，而不是从 tail 开始。用
	// TrimPartialRune 回退这个起点会检查错误的字节，并可能让
	// 后缀从 rune 中间开始；应把它向前移到下一个
	// rune 边界，使保留的后缀始终是有效 UTF-8。
	tailStart := len(text) - tail
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	// 尾部只前进到最近的字素簇边界，确保后缀不会以悬空的
	// 组合标记/ZWJ 开头；与头部一样，保留字节只减不增。
	tailStart = textutil.ClusterBoundaryAfter(text, tailStart)
	return text[:head] + clipMarkerLegacy + text[tailStart:]
}

// clipMarkerLegacy 是 clipTo 的历史标记，长度固定，且作为子串保留在
// 默认帧的损失头版本中，使所有只做 Contains 判断的调用方继续工作。
const clipMarkerLegacy = "\n... [truncated] ...\n"

// ---- #36 裁剪损失头：原始 token 数与总行数 ----

// lossHeader 生成有界的裁剪损失头。token 估算复用 CJK/emoji 感知的
// tokenize.Estimate；行数按换行符计数（末行无换行也算一行）。
// 头本身只有几个十进制整数与固定文案，可安全写进日志与模型上下文。
func lossHeader(raw string) string {
	return fmt.Sprintf("[truncated: original ~%d tokens, %d lines, kept head+tail]",
		tokenize.Estimate(raw), strings.Count(raw, "\n")+1)
}

// clipWithLossHeader 在 clipTo 的裁剪帧上附加损失头。插入点选在旧标记
// 之后，使 "\n... [truncated] ...\n" 仍作为连续子串存在，保持向后兼容。
// 未发生裁剪时原样返回，不给短输出平白添加提示。
func clipWithLossHeader(text string, limit int) string {
	out := clipTo(text, limit)
	if out == text || !strings.Contains(out, clipMarkerLegacy) {
		return out
	}
	return strings.Replace(out, clipMarkerLegacy, clipMarkerLegacy+lossHeader(text)+"\n", 1)
}

// ---- #37 JSON 转义感知的输出预算 ----

const (
	// jsonSerializationDenominator 与 5 组合得到 1.2 系数，预留
	// JSON 字符串转义（/、\、换行、引号）带来的膨胀。
	jsonSerializationNumerator   = 6
	jsonSerializationDenominator = 5
	// maxJSONShrinkRounds 限制收缩轮数，避免病态输入导致长循环。
	maxJSONShrinkRounds = 4
)

// clipWithJSONBudget 对裁剪结果做 json.Marshal 探测。1.2 系数用于
// 序列化后的允许上限（codex 的 with_serialization_allowance），原始
// 裁剪预算始终从 limit 起步，保证 40 KB 输出仍按旧路径被截断。
// 超限则按 codex 比例公式逐轮收缩原始预算，最多 4 轮。
func clipWithJSONBudget(text string, limit int) string {
	if limit <= 0 {
		limit = maxToolOutput
	}
	// allowance = limit * 6 / 5，预留 JSON 转义膨胀。
	allowance := limit + limit/jsonSerializationDenominator
	budget := limit
	out := clipWithLossHeader(text, budget)
	for round := 0; round < maxJSONShrinkRounds; round++ {
		encoded, err := json.Marshal(out)
		if err != nil {
			return clipTo(text, limit)
		}
		if len(encoded) <= allowance {
			return out
		}
		// codex: preview_budget = preview_budget * max_bytes / truncated_len。
		next := budget * allowance / len(encoded)
		if next >= budget {
			next = budget - 1
		}
		if next < 1 {
			next = 1
		}
		budget = next
		out = clipWithLossHeader(text, budget)
	}
	return out
}

// ---- #33 会话内易失工具结果缓存 ----

const (
	// toolResultCacheThreshold 是启用缓存的"超大"阈值。取 1 MiB，
	// 明显高于 maxToolOutput 与常见工具输出，因此常规裁剪帧逐字节不变。
	toolResultCacheThreshold = 1 << 20
	// toolResultCacheByteBudget 是会话缓存的总字节上限（16 MiB）。
	toolResultCacheByteBudget = 16 << 20
	// toolResultCacheIdleRounds 是连续多少轮未被 read 后淘汰的阈值。
	toolResultCacheIdleRounds = 5
	toolResultCacheURIPrefix  = "argent://cache/"
	toolResultCacheURISuffix  = ".result.txt"
	// toolResultCacheMissHint 明确要求模型不要重放副作用来取回输出。
	toolResultCacheMissHint = "argent://cache 条目已过期（会话内结果缓存空闲 " +
		"5 轮后淘汰）；请勿重放有副作用的操作来取回输出，改用只读工具重新获取。" +
		" DO NOT repeat side-effecting actions to recover output."
)

// cachedToolResult 是缓存中的一条记录；lastUsed 记录最后一次被 read
// 命中时的轮次，用于空闲淘汰。
type cachedToolResult struct {
	text     string
	bytes    int
	lastUsed int
}

// sessionToolResultCache 是纯标准库的 LRU + 字节预算缓存。所有方法
// 持锁，clip 与 read 可能被并发调用。
type sessionToolResultCache struct {
	mu     sync.Mutex
	items  map[string]cachedToolResult
	lru    []string
	round  int
	nextID uint64
	budget int
}

func newSessionToolResultCache(budget int) *sessionToolResultCache {
	if budget <= 0 {
		budget = toolResultCacheByteBudget
	}
	return &sessionToolResultCache{
		items:  make(map[string]cachedToolResult),
		budget: budget,
	}
}

// store 保存 text 并返回 argent://cache/<id>.result.txt。单条超过总
// 预算时不缓存（返回空串），由调用方回落到普通裁剪。
func (c *sessionToolResultCache) store(text string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(text) > c.budget {
		return ""
	}
	c.nextID++
	key := fmt.Sprintf("%016x", c.nextID)
	c.items[key] = cachedToolResult{text: text, bytes: len(text), lastUsed: c.round}
	c.lru = append(c.lru, key)
	for c.bytesLocked() > c.budget && len(c.lru) > 0 {
		c.evictLocked()
	}
	return toolResultCacheURIPrefix + key + toolResultCacheURISuffix
}

// load 解析 URI。handled 为 false 表示这不是缓存 URI，调用方应走原
// 文件读取路径；为 true 时 result 要么是缓存正文，要么是过期的提示。
func (c *sessionToolResultCache) load(uri string) (result string, handled bool) {
	if !strings.HasPrefix(uri, toolResultCacheURIPrefix) ||
		!strings.HasSuffix(uri, toolResultCacheURISuffix) {
		return "", false
	}
	key := strings.TrimSuffix(strings.TrimPrefix(uri, toolResultCacheURIPrefix), toolResultCacheURISuffix)
	if key == "" || strings.ContainsAny(key, `/\\`) {
		return toolResultCacheMissHint, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[key]
	if !ok {
		return toolResultCacheMissHint, true
	}
	item.lastUsed = c.round
	c.items[key] = item
	for i, k := range c.lru {
		if k == key {
			c.lru = append(append([]string{}, c.lru[:i]...), c.lru[i+1:]...)
			c.lru = append(c.lru, key) // 最近使用移到队尾
			break
		}
	}
	return item.text, true
}

// advance 推进一轮并淘汰连续 toolResultCacheIdleRounds 未被 read 的条目。
func (c *sessionToolResultCache) advance() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.round++
	keep := c.lru[:0]
	for _, key := range c.lru {
		if c.round-c.items[key].lastUsed >= toolResultCacheIdleRounds {
			delete(c.items, key)
			continue
		}
		keep = append(keep, key)
	}
	c.lru = keep
}

func (c *sessionToolResultCache) bytesLocked() int {
	total := 0
	for _, item := range c.items {
		total += item.bytes
	}
	return total
}

func (c *sessionToolResultCache) evictLocked() {
	if len(c.lru) == 0 {
		return
	}
	oldest := c.lru[0]
	c.lru = c.lru[1:]
	delete(c.items, oldest)
}

var defaultSessionCache = newSessionToolResultCache(toolResultCacheByteBudget)

type toolResultCacheKey struct{}

// withToolResultCache 允许调用方替换默认缓存（测试与后续 loop 接线用）。
func withToolResultCache(ctx context.Context, c *sessionToolResultCache) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, toolResultCacheKey{}, c)
}

func toolResultCacheFrom(ctx context.Context) *sessionToolResultCache {
	if c, ok := ctx.Value(toolResultCacheKey{}).(*sessionToolResultCache); ok && c != nil {
		return c
	}
	return defaultSessionCache
}

// cacheOversizedToolResult 是 clip 内的缓存钩子：只有超过阈值的输出
// 才被缓存；缓存成功时返回带 URI 的有界正文，handled=true。
func cacheOversizedToolResult(ctx context.Context, text string) (string, bool) {
	if len(text) <= toolResultCacheThreshold {
		return "", false
	}
	c := toolResultCacheFrom(ctx)
	uri := c.store(text)
	if uri == "" {
		return "", false
	}
	// 每次缓存一条新结果视作一轮推进，使空闲淘汰在默认路径上生效；
	// 后续 loop 接线可改为按模型轮次精确调用 advance。
	c.advance()
	preview := text
	if len(preview) > 512 {
		preview = preview[:512]
	}
	return fmt.Sprintf("[tool result cached] %d bytes; read %s to retrieve.\nhead: %s\n",
		len(text), uri, preview), true
}

// loadCachedToolResult 供 read 工具在 resolve 之前调用，解析 argent://cache URI。
func loadCachedToolResult(ctx context.Context, uri string) (string, bool) {
	return toolResultCacheFrom(ctx).load(uri)
}

// trimPartialRune 是包内别名，保留给 web_fetch/web_search/output_spill
// 等既有调用方；真正的实现已下沉到 textutil.TrimPartialRune，消除重复。
func trimPartialRune(text string, offset int) int {
	return textutil.TrimPartialRune(text, offset)
}
