package llm

import "sync"

// 本文件提供一个纯内存、并发安全的递归深度预算器，用于给命令替换（$()）
// 之类的嵌套展开加一道 defense-in-depth 上限。它不接触任何协议、连接或 I/O，
// 也没有内置调用方：是否被接线完全由调用方决定，因此默认不引入任何行为变化。
// 它只依赖标准库。

// defaultSubstMaxDepth 是 maxDepth<=0 时采用的默认嵌套上限。取 32 远高于
// 正常 shell 命令替换的嵌套层数，因此默认配置下不会影响现有展开，只在
// 恶意或病态的深度递归时才触发保护。
const defaultSubstMaxDepth = 32

// SubstBudget 对嵌套展开的深度做有界记账。
//
// 字段语义（均由 mu 保护，读写都需持锁）：
//   - depth: 当前已进入但尚未退出的嵌套层数；
//   - maxDepth: 允许的嵌套上限，<=0 表示尚未初始化，首次 Enter 时套用默认值；
//   - calls: Enter 被调用的总次数（含因超限被拒绝的调用），仅供诊断；
//   - exhausted: 是否曾因超过上限而拒绝过进入，一旦置位保持 true。
//
// 零值可直接复用（首次 Enter 会套用默认上限）。所有方法都可被多个 goroutine
// 并发调用：Enter/Exit 是"改深度 + 判上限 + 置标志"的复合操作，用一把互斥锁
// 包住，使整组动作原子化，避免检查与递增之间出现竞态。
type SubstBudget struct {
	mu sync.Mutex

	depth     int
	maxDepth  int
	calls     int
	exhausted bool
}

// NewSubstBudget 构造一个上限为 maxDepth 的预算器。
// maxDepth<=0 时改用 defaultSubstMaxDepth。
func NewSubstBudget(maxDepth int) *SubstBudget {
	if maxDepth <= 0 {
		maxDepth = defaultSubstMaxDepth
	}
	return &SubstBudget{maxDepth: maxDepth}
}

// Enter 尝试进入一层嵌套。它递增深度与调用计数，随后判断当前深度是否超过
// 上限：未超过返回 true，调用方应继续执行展开并在结束时配对调用 Exit；
// 超过则置 exhausted 为 true 并返回 false，调用方应停止继续深入且【不要】
// 为这次失败的 Enter 调用 Exit。
//
// 即便返回 false，depth 也已按契约递增（见字段说明），因为超限后不会再有
// 新的进入，残留的计数不影响预算器的保护语义。
func (b *SubstBudget) Enter() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureInit()
	b.calls++
	b.depth++
	if b.depth > b.maxDepth {
		b.exhausted = true
		return false
	}
	return true
}

// Exit 退出一层嵌套，把深度减一。深度不会低于 0，因此多调用或提前调用
// Exit 都不会破坏记账（但正确用法仍是一次成功 Enter 配一次 Exit）。
// Exit 不清空 exhausted：展开回收后仍需能诊断出曾否触顶。
func (b *SubstBudget) Exit() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.depth > 0 {
		b.depth--
	}
}

// Exhausted 报告是否曾因超过上限而拒绝过进入。该标志 sticky，
// 一旦置位直到预算器被丢弃都不会回到 false。
func (b *SubstBudget) Exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.exhausted
}

// Calls 返回 Enter 被调用的总次数（含被拒绝的调用），用于诊断展开规模。
func (b *SubstBudget) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.calls
}

// ensureInit 让零值预算器可用：maxDepth 未设置时套用默认上限。
// 调用方必须已持有 b.mu。
func (b *SubstBudget) ensureInit() {
	if b.maxDepth <= 0 {
		b.maxDepth = defaultSubstMaxDepth
	}
}
