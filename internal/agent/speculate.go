package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/agentpolicy"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件提供"推测执行"的 agent 侧编排原语：计算调用指纹、缓存预跑结果、
// 以及判定一次工具调用是否值得预跑。全部是纯逻辑与内存状态，零 I/O、
// 零调用方、默认 opt-in 关闭（没有任何内置代码把 enabled 置为 true）。
//
// 它刻意不接入 loop 的任何检查点，也不改动 runBefore* 钩子；接线层若将来
// 需要启用推测执行，可自行把 llm.ToolDeltaAccumulator 的快照喂给本文件的
// 决策函数，并在安全链之外复用 SpecCache。

// SpeculativeFingerprint 返回一次工具调用的稳定指纹。
//
// 公式为 sha256(name + "\x00" + NormalizeArgs(args))：NUL 作为分隔符避免
// name 与 args 的拼接产生歧义（例如 name="a", args="b" 与 name="ab", args=""
// 不会碰撞）；args 先经 agentpolicy.NormalizeArgs 规范化，使键顺序或空白
// 不同但语义相同的 JSON 参数得到同一指纹。返回小写十六进制字符串。
func SpeculativeFingerprint(name, args string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + agentpolicy.NormalizeArgs(args)))
	return hex.EncodeToString(sum[:])
}

// SpecResult 是一次推测预跑的产物：字符串输出与可能的错误。
// Err 与 Output 可以共存（工具在产出部分输出后失败），调用方按需取用。
type SpecResult struct {
	Output string
	Err    error
}

// specCacheEntry 是缓存中的一条记录，附带写入时所属的轮次。
type specCacheEntry struct {
	round  uint64
	result SpecResult
}

// SpecCache 是按指纹存取预跑结果的并发安全缓存。
//
// 缓存以"轮次"管理有效性：Set 把结果写入当前轮，InvalidateRound 推进轮次，
// 使之前所有轮的条目一次性作废（O(1)）。作废的条目不立即逐条删除，而是在
// 后续 Get/Lookup 触及它们时惰性清除，因此调用方无需为整轮作废付出遍历代价。
// 零值即可直接使用；所有导出方法都接受 nil 接收者（nil 时执行空操作）。
type SpecCache struct {
	mu      sync.Mutex
	round   uint64
	entries map[string]specCacheEntry
}

// specLookupStatus 区分一次查找的三种结果，供 Lookup 生成 SpeculativeOutcome。
type specLookupStatus uint8

const (
	specMiss  specLookupStatus = iota // 从未写入，或已被惰性清除
	specHit                           // 命中当前轮条目
	specStale                         // 命中旧轮条目，已被整轮作废
)

// lookup 是 Get 与 Lookup 共用的内部查找：返回条目与状态，并在发现旧轮
// 条目时顺手删除它（惰性回收）。调用方必须持有 c.mu 之外的锁边界由本方法
// 自身负责，因此它可安全地在任何路径上调用。
func (c *SpecCache) lookup(fp string) (SpecResult, specLookupStatus) {
	if c == nil {
		return SpecResult{}, specMiss
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[fp]
	if !ok {
		return SpecResult{}, specMiss
	}
	if e.round != c.round {
		delete(c.entries, fp)
		return SpecResult{}, specStale
	}
	return e.result, specHit
}

// Get 读取指纹 fp 对应的预跑结果。
// 仅当结果属于当前轮且确实存在时返回 (result, true)；未命中或已被
// 整轮作废都返回 (零值, false)，调用方据此决定是否需要真正预跑。
func (c *SpecCache) Get(fp string) (SpecResult, bool) {
	result, status := c.lookup(fp)
	return result, status == specHit
}

// Set 把指纹 fp 的预跑结果写入当前轮。同一指纹重复写入以最后一次为准。
func (c *SpecCache) Set(fp string, result SpecResult) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[string]specCacheEntry)
	}
	c.entries[fp] = specCacheEntry{round: c.round, result: result}
}

// InvalidateRound 作废整轮缓存：推进轮次，使此前 Set 的所有条目在新一轮
// 中全部失效。它是一个常量时间的计数递增，不遍历也不删除条目；旧条目会在
// 后续 lookup 中被惰性清除。
func (c *SpecCache) InvalidateRound() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.round++
}

// SpeculativeOutcome 描述一次推测预跑的处置结果。
// Hit 与 Invalidated 互斥：前者表示可复用当前轮缓存，后者表示曾命中但
// 已被整轮作废；两者都为假且 Eligible 为真时表示需要真正预跑（miss）。
type SpeculativeOutcome struct {
	Fingerprint string     // 该调用的指纹；Skip 时为空
	Eligible    bool       // ShouldSpeculate 是否放行
	Hit         bool       // 命中当前轮缓存
	Invalidated bool       // 命中旧轮条目，已被作废
	Result      SpecResult // Hit 时携带的预跑结果
}

// specOutcomeSkip 等常量用文字表述 SpeculativeOutcome 的四种典型形态，
// 便于调试与测试断言时对照，不参与运行时逻辑。
const (
	specOutcomeSkip        = "skip"
	specOutcomeMiss        = "miss"
	specOutcomeHit         = "hit"
	specOutcomeInvalidated = "invalidated"
)

// String 以简短标签描述该处置结果，仅用于日志与测试可读性。
func (o SpeculativeOutcome) String() string {
	switch {
	case !o.Eligible:
		return specOutcomeSkip
	case o.Invalidated:
		return specOutcomeInvalidated
	case o.Hit:
		return specOutcomeHit
	default:
		return specOutcomeMiss
	}
}

// ShouldSpeculate 判定一次工具调用是否值得推测执行。
//
// 规则（前者优先、fail-closed）：
//  1. enabled 为 false → false。推测执行是 opt-in 的，默认关闭；
//  2. isReadOnly 为 nil、或工具名为空 → false；只读白名单由调用方注入，
//     agent 不 import tools，因此无法自行判定，缺失判据时一律拒绝；
//  3. 其余情况返回 isReadOnly(delta.Name)。
//
// 这是纯决策函数：不读缓存、不执行工具、不产生任何副作用。
func ShouldSpeculate(delta llm.ToolCallDelta, isReadOnly func(name string) bool, enabled bool) bool {
	if !enabled || isReadOnly == nil || delta.Name == "" {
		return false
	}
	return isReadOnly(delta.Name)
}

// Lookup 把决策门与缓存查找组合成一次完整的处置结果。
//
// 它先调用 ShouldSpeculate：不放行时返回 Skip（零值 Outcome，Fingerprint 为空）；
// 放行时计算指纹并查缓存，据此返回 Hit（携带结果）、Invalidated 或 Miss。
// 该方法本身仍是纯编排：只读内存状态，不执行工具、不写任何外部系统。
func (c *SpecCache) Lookup(delta llm.ToolCallDelta, isReadOnly func(name string) bool, enabled bool) SpeculativeOutcome {
	if !ShouldSpeculate(delta, isReadOnly, enabled) {
		return SpeculativeOutcome{}
	}

	out := SpeculativeOutcome{
		Fingerprint: SpeculativeFingerprint(delta.Name, delta.Arguments),
		Eligible:    true,
	}
	result, status := c.lookup(out.Fingerprint)
	switch status {
	case specHit:
		out.Hit = true
		out.Result = result
	case specStale:
		out.Invalidated = true
	}
	return out
}
