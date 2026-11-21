package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ErrRaceToolNotReadOnly 表示竞速候选试图调用未被 ReadOnly 许可的工具。
// 调用方可用 errors.Is 匹配它。
var ErrRaceToolNotReadOnly = errors.New("race candidate tool is not read-only")

// RaceCandidate 是一个竞速候选的最终快照。
// Slot 是预分配的下标，ID 是稳定标识；两者都与完成顺序无关，
// 所以同一组候选无论谁先跑完，Select 返回的 all 切片都逐字节一致。
type RaceCandidate struct {
	Slot   int
	ID     string
	Result Result
	Err    error
}

// RaceRubric 给候选打分。返回的理由文本只用于诊断，不参与排序；
// 排序只看分数、Slot、ID，因此结果完全确定。
type RaceRubric interface {
	Score(c RaceCandidate) (int, string)
}

// raceDefaultRubric 是包内默认评分器。它只读取候选自身的最终文本：
// 非空且不含 FAIL 得 1 分，其余得 0 分；理由字符串是固定常量，
// 因此对同一候选反复打分永远得到相同结论（确定性），也不依赖顺序或时间。
type raceDefaultRubric struct{}

// Score 实现 RaceRubric。
func (raceDefaultRubric) Score(c RaceCandidate) (int, string) {
	if c.Err != nil {
		return 0, "race: candidate errored"
	}
	text := strings.TrimSpace(c.Result.Text)
	if text == "" {
		return 0, "race: empty answer"
	}
	if raceTextHasFAIL(text) {
		return 0, "race: answer contains FAIL"
	}
	return 1, "race: non-empty answer without FAIL"
}

// raceTextHasFAIL 报告文本中是否出现 FAIL（大小写不敏感）。
// 用子串而不是首词判断，避免 "Sorry, FAIL." 这类回答被当成合格答案。
func raceTextHasFAIL(text string) bool {
	return strings.Contains(strings.ToUpper(text), "FAIL")
}

// RaceSelector 并行运行 N 个只读候选，按 rubric 选出唯一胜者。
// 它是一个纯原语：没有生产调用方，N<=1 时退化为一次普通运行，
// 因此现有运行的对外行为逐字节不变。
type RaceSelector struct {
	// N 是候选数量。<=1 退化为单次运行：不切分预算、不开并发。
	N int
	// Rubric 为 nil 时使用包内默认评分器 raceDefaultRubric。
	Rubric RaceRubric
	// Child 与 SubagentConfig.NewChild 同形的测试/接线接缝；
	// nil 时使用 &Runner{Opts: opts}。Child 按 Slot 升序依次调用，
	// 注入方可以用调用序号识别候选。
	Child func(Options) ChildRunner
	// MaxTotal 是所有候选的聚合预算上限；零值时沿用 base.Budget。
	// N>1 时先按 sliceForChild 切分，再把累计字段按候选数分摊，
	// 使所有候选的预算之和不超过 MaxTotal。
	MaxTotal Budget
	// ReadOnly 判定工具名是否只读。非 nil 时候选只能调用返回 true 的工具；
	// nil 表示不做限制（默认关闭）。
	ReadOnly func(string) bool
}

// raceScoredCandidate 把候选与其 rubric 分数绑在一起，供确定性比较使用。
type raceScoredCandidate struct {
	candidate RaceCandidate
	score     int
}

// raceScoredLess 定义全序：分数降序、Slot 升序、ID 字典序。
// 三个键足以区分所有候选（Slot 在 Select 中唯一），所以选择结果与
// 候选完成顺序、切片底层顺序都无关。
func raceScoredLess(a, b raceScoredCandidate) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.candidate.Slot != b.candidate.Slot {
		return a.candidate.Slot < b.candidate.Slot
	}
	return a.candidate.ID < b.candidate.ID
}

// racePickWinner 从候选中选出唯一胜者。空切片返回零值候选。
func racePickWinner(candidates []RaceCandidate, rubric RaceRubric) RaceCandidate {
	if len(candidates) == 0 {
		return RaceCandidate{}
	}
	best := raceScoredCandidate{candidate: candidates[0]}
	if rubric == nil {
		rubric = raceDefaultRubric{}
	}
	best.score, _ = rubric.Score(best.candidate)
	for i := 1; i < len(candidates); i++ {
		cur := raceScoredCandidate{candidate: candidates[i]}
		cur.score, _ = rubric.Score(cur.candidate)
		if raceScoredLess(cur, best) {
			best = cur
		}
	}
	return best.candidate
}

// raceCandidateID 生成与 Slot 一一对应的稳定标识。
func raceCandidateID(slot int) string {
	return fmt.Sprintf("candidate-%d", slot)
}

// raceChildBudget 由聚合上限派生单个候选的预算。
// N<=1 时无需分摊，原样返回；N>1 时先复用 sliceForChild 的"每项减半"语义，
// 再把累计字段（总 token、花费、声明输出）按候选数分摊，
// 保证所有候选的累计字段之和不超过传入的上限。
func raceChildBudget(total Budget, n int) Budget {
	if n <= 1 {
		return total
	}
	b := total.sliceForChild()
	if b.MaxTotalTokens > 0 {
		b.MaxTotalTokens = max(1, b.MaxTotalTokens/n)
	}
	if b.MaxCostUSD > 0 {
		b.MaxCostUSD /= float64(n)
	}
	if b.DeclaredTurnTokens > 0 {
		b.DeclaredTurnTokens = max(int64(1), b.DeclaredTurnTokens/int64(n))
	}
	return b
}

// raceRecord 是候选 Record 中的一条缓冲记录，保留 kind 与原始 payload。
type raceRecord struct {
	kind    string
	payload any
}

// raceRecordBuffer 是候选专用的记录缓冲。候选之间的写入互不干扰，
// 只有胜者的缓冲会在 Select 结束时回灌到 base.Record。
type raceRecordBuffer struct {
	mu      sync.Mutex
	records []raceRecord
}

// Append 实现 Recorder。
func (b *raceRecordBuffer) Append(kind string, payload any) error {
	b.mu.Lock()
	b.records = append(b.records, raceRecord{kind: kind, payload: payload})
	b.mu.Unlock()
	return nil
}

// snapshot 在锁下复制已缓冲的记录，避免回灌时与候选内部的并发写入竞争。
func (b *raceRecordBuffer) snapshot() []raceRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]raceRecord(nil), b.records...)
}

// raceReadOnlyTools 是候选看到的只读工具视图：公布的规格与可执行调用
// 都只保留 ReadOnly 判定为 true 的工具名。
type raceReadOnlyTools struct {
	inner ToolRunner
	allow func(string) bool
}

// Specs 实现 ToolRunner，只对模型公布只读工具。
func (t raceReadOnlyTools) Specs() []llm.ToolSpec {
	if t.inner == nil {
		return nil
	}
	specs := t.inner.Specs()
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if t.allow != nil && !t.allow(spec.Function.Name) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// Run 实现 ToolRunner，拒绝非只读工具并返回可用 errors.Is 匹配的错误。
func (t raceReadOnlyTools) Run(ctx context.Context, name, args string) (string, error) {
	if t.allow != nil && !t.allow(name) {
		return "", fmt.Errorf("%w: %q", ErrRaceToolNotReadOnly, name)
	}
	if t.inner == nil {
		return "", fmt.Errorf("agent: race candidate has no tools")
	}
	return t.inner.Run(ctx, name, args)
}

// Select 运行候选出胜者，并把胜者的记录回灌给 base.Record。
// 返回的 all 按 Slot 升序排列；winner 是 all 中按 rubric 全序比较的最优项。
// 候选的 Record 是独立缓冲，失败候选的记录绝不会写进 base.Record。
func (sel RaceSelector) Select(ctx context.Context, base Options, prompt string) (winner RaceCandidate, all []RaceCandidate, err error) {
	n := sel.N
	if n < 1 {
		n = 1
	}

	// 聚合预算来源：显式 MaxTotal 优先，否则沿用父级预算。
	src := sel.MaxTotal
	if src == (Budget{}) {
		src = base.Budget
	}
	childBudget := raceChildBudget(src, n)

	rubric := sel.Rubric
	if rubric == nil {
		rubric = raceDefaultRubric{}
	}

	// 候选只能看到被标记为只读的工具；nil 判定函数时保持原样。
	var tools ToolRunner = base.Tools
	if sel.ReadOnly != nil {
		tools = raceReadOnlyTools{inner: base.Tools, allow: sel.ReadOnly}
	}

	newChild := sel.Child
	if newChild == nil {
		newChild = func(opts Options) ChildRunner { return &Runner{Opts: opts} }
	}

	// 按 Slot 预分配结果槽与记录缓冲；每个 goroutine 只写自己的下标，
	// 因此不需要为 candidates / buffers 本身加锁。
	all = make([]RaceCandidate, n)
	buffers := make([]*raceRecordBuffer, n)
	children := make([]ChildRunner, n)
	for slot := 0; slot < n; slot++ {
		buffers[slot] = &raceRecordBuffer{}
		opts := base
		// 红线：绝不把父级 Record 透传给候选，否则失败候选也会污染会话。
		opts.Record = buffers[slot]
		opts.Budget = childBudget
		opts.Tools = tools
		children[slot] = newChild(opts)
	}

	runOne := func(slot int) {
		res, runErr := children[slot].Run(ctx, prompt)
		all[slot] = RaceCandidate{
			Slot:   slot,
			ID:     raceCandidateID(slot),
			Result: res,
			Err:    runErr,
		}
	}

	if n == 1 {
		// N<=1 退化为单次运行：不进 goroutine、不经过信号量。
		runOne(0)
	} else {
		conc := n
		if base.MaxParallel > 0 && base.MaxParallel < conc {
			conc = base.MaxParallel
		}
		if conc < 1 {
			conc = 1
		}
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		for slot := 0; slot < n; slot++ {
			select {
			case <-ctx.Done():
				// 取消后未启动的槽位保留预分配的 Slot/ID 与取消错误。
				all[slot] = RaceCandidate{Slot: slot, ID: raceCandidateID(slot), Err: ctx.Err()}
				continue
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(s int) {
				defer wg.Done()
				defer func() { <-sem }()
				runOne(s)
			}(slot)
		}
		wg.Wait()
	}

	winner = racePickWinner(all, rubric)

	// 只回灌胜者的记录。base.Record 可能是 Recorder、函数或 nil，
	// normaliseRecorder 复用 Options.Record 历史上支持过的全部形态。
	if rec := normaliseRecorder(base.Record); rec != nil && buffers[winner.Slot] != nil {
		for _, r := range buffers[winner.Slot].snapshot() {
			_ = rec.Append(r.kind, r.payload)
		}
	}
	// err 反映胜者自身的失败；评分更高的候选由 rubric 负责挑选。
	err = winner.Err
	return winner, all, err
}
