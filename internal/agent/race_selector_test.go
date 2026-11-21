package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// raceTestChild 是按 Slot 编排结果的伪 ChildRunner。
type raceTestChild struct {
	slot   int
	result Result
	err    error
	opts   Options
	onRun  func(slot int, opts Options)
}

func (c *raceTestChild) Run(ctx context.Context, prompt string) (Result, error) {
	if c.onRun != nil {
		c.onRun(c.slot, c.opts)
	}
	return c.result, c.err
}

// raceChildFactory 依次构建候选。Select 保证 Child 按 Slot 升序调用，
// 因此第 k 次调用对应 Slot k，伪实现可据此返回不同结果。
type raceChildFactory struct {
	mu      sync.Mutex
	built   []*raceTestChild
	opts    []Options
	onBuild func(c *raceTestChild)
}

func (f *raceChildFactory) child(opts Options) ChildRunner {
	f.mu.Lock()
	c := &raceTestChild{slot: len(f.built), opts: opts}
	f.built = append(f.built, c)
	f.opts = append(f.opts, opts)
	onBuild := f.onBuild
	f.mu.Unlock()
	if onBuild != nil {
		onBuild(c)
	}
	return c
}

// raceScoreRubric 按最终文本给出固定分数，用于隔离排序逻辑。
type raceScoreRubric map[string]int

func (r raceScoreRubric) Score(c RaceCandidate) (int, string) {
	s := r[c.Result.Text]
	return s, fmt.Sprintf("score:%d", s)
}

func raceToolSpec(name string) llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Function.Name = name
	return spec
}

func TestRaceSelectorCompletionOrderDoesNotAffectWinner(t *testing.T) {
	const n = 4
	gates := make([]chan struct{}, n)
	for i := range gates {
		gates[i] = make(chan struct{})
	}
	finished := make(chan int, n)

	factory := &raceChildFactory{}
	factory.onBuild = func(c *raceTestChild) {
		c.result = Result{Text: fmt.Sprintf("answer-%d", c.slot)}
		c.onRun = func(slot int, _ Options) {
			<-gates[slot]
			finished <- slot
		}
	}
	sel := RaceSelector{
		N:      n,
		Child:  factory.child,
		Rubric: raceScoreRubric{"answer-0": 9}, // 最后完成的候选得分最高
	}

	type outcome struct {
		winner RaceCandidate
		all    []RaceCandidate
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		w, a, e := sel.Select(context.Background(), Options{}, "prompt")
		done <- outcome{w, a, e}
	}()

	// 强制按 3,2,1,0 完成，与 Slot 顺序相反。
	for _, slot := range []int{3, 2, 1, 0} {
		close(gates[slot])
		select {
		case got := <-finished:
			if got != slot {
				t.Fatalf("完成顺序 = %d, 期望 %d", got, slot)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("候选未在规定时间内完成")
		}
	}
	out := <-done
	if out.err != nil {
		t.Fatalf("Select() err = %v, 期望 nil", out.err)
	}
	if out.winner.Slot != 0 || out.winner.Result.Text != "answer-0" {
		t.Fatalf("winner = %+v, 期望由评分选出 Slot 0（而非最先完成的 Slot 3）", out.winner)
	}
	if len(out.all) != n {
		t.Fatalf("len(all) = %d, 期望 %d", len(out.all), n)
	}
	for i, c := range out.all {
		if c.Slot != i || c.ID != fmt.Sprintf("candidate-%d", i) {
			t.Fatalf("all[%d] = %+v, 期望按 Slot 预分配", i, c)
		}
	}
}

func TestRacePickWinnerTieBreak(t *testing.T) {
	rubric := raceScoreRubric{"same": 5, "low": 1, "high": 9}
	tests := []struct {
		name       string
		candidates []RaceCandidate
		wantSlot   int
		wantID     string
	}{
		{
			name: "同分时 Slot 升序优先",
			candidates: []RaceCandidate{
				{Slot: 2, ID: "z", Result: Result{Text: "same"}},
				{Slot: 1, ID: "y", Result: Result{Text: "same"}},
				{Slot: 0, ID: "x", Result: Result{Text: "same"}},
			},
			wantSlot: 0,
			wantID:   "x",
		},
		{
			name: "同 Slot 时 ID 字典序优先",
			candidates: []RaceCandidate{
				{Slot: 7, ID: "z", Result: Result{Text: "same"}},
				{Slot: 7, ID: "b", Result: Result{Text: "same"}},
				{Slot: 7, ID: "a", Result: Result{Text: "same"}},
			},
			wantSlot: 7,
			wantID:   "a",
		},
		{
			name: "分数降序压过 Slot 升序",
			candidates: []RaceCandidate{
				{Slot: 0, ID: "a", Result: Result{Text: "low"}},
				{Slot: 9, ID: "z", Result: Result{Text: "high"}},
			},
			wantSlot: 9,
			wantID:   "z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := racePickWinner(tc.candidates, rubric)
			if got.Slot != tc.wantSlot || got.ID != tc.wantID {
				t.Fatalf("racePickWinner() = %+v, 期望 Slot=%d ID=%q", got, tc.wantSlot, tc.wantID)
			}
		})
	}
	if got := racePickWinner(nil, rubric); got.Slot != 0 || got.ID != "" || got.Err != nil || got.Result.Text != "" {
		t.Fatalf("racePickWinner(nil) = %+v, 期望零值", got)
	}
}

func TestRaceSelectorEqualScoresPickLowestSlot(t *testing.T) {
	factory := &raceChildFactory{}
	factory.onBuild = func(c *raceTestChild) {
		c.result = Result{Text: "same"}
	}
	sel := RaceSelector{N: 5, Child: factory.child, Rubric: raceScoreRubric{"same": 3}}
	w, all, err := sel.Select(context.Background(), Options{}, "p")
	if err != nil {
		t.Fatalf("Select() err = %v", err)
	}
	if w.Slot != 0 || len(all) != 5 {
		t.Fatalf("winner/all = %+v/%d, 期望 Slot 0 与 5 个候选", w, len(all))
	}
}

func TestRaceSelectorReadOnlyConstraint(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{raceToolSpec("read"), raceToolSpec("write")}}
	violations := make(chan string, 16)

	factory := &raceChildFactory{}
	factory.onBuild = func(c *raceTestChild) {
		c.result = Result{Text: "ok"}
		c.onRun = func(_ int, opts Options) {
			if _, ok := opts.Record.(*raceRecordBuffer); !ok {
				violations <- "父级 Record 被透传给候选"
			}
			var exposed []string
			for _, spec := range opts.Tools.Specs() {
				exposed = append(exposed, spec.Function.Name)
			}
			if fmt.Sprint(exposed) != "[read]" {
				violations <- fmt.Sprintf("候选看到的工具 = %v, 期望 [read]", exposed)
			}
			if _, err := opts.Tools.Run(context.Background(), "read", ""); err != nil {
				violations <- "只读工具被拒绝: " + err.Error()
			}
			if _, err := opts.Tools.Run(context.Background(), "write", ""); !errors.Is(err, ErrRaceToolNotReadOnly) {
				violations <- fmt.Sprintf("变更工具未被拒绝: %v", err)
			}
		}
	}
	sel := RaceSelector{
		N:        2,
		Child:    factory.child,
		ReadOnly: func(name string) bool { return name == "read" },
	}
	if _, _, err := sel.Select(context.Background(), Options{Tools: tools}, "p"); err != nil {
		t.Fatalf("Select() err = %v", err)
	}
	select {
	case v := <-violations:
		t.Fatal(v)
	default:
	}
	if got := fmt.Sprint(tools.names()); got != "[read read]" {
		t.Fatalf("底层工具收到 %s, 期望 [read read]", got)
	}
}

func TestRaceSelectorPersistsOnlyWinner(t *testing.T) {
	var mu sync.Mutex
	var rows []raceRecord
	rec := RecorderFunc(func(kind string, payload any) error {
		mu.Lock()
		rows = append(rows, raceRecord{kind: kind, payload: payload})
		mu.Unlock()
		return nil
	})
	violations := make(chan string, 16)

	factory := &raceChildFactory{}
	factory.onBuild = func(c *raceTestChild) {
		c.result = Result{Text: fmt.Sprintf("answer-%d", c.slot)}
		c.onRun = func(slot int, opts Options) {
			buf, ok := opts.Record.(*raceRecordBuffer)
			if !ok {
				violations <- fmt.Sprintf("候选 %d 未拿到独立 Record 缓冲", slot)
				return
			}
			_ = buf.Append("candidate", slot)
			_ = buf.Append("detail", fmt.Sprintf("s%d", slot))
		}
	}
	sel := RaceSelector{N: 3, Child: factory.child, Rubric: raceScoreRubric{"answer-1": 5}}
	w, all, err := sel.Select(context.Background(), Options{Record: rec}, "p")
	if err != nil {
		t.Fatalf("Select() err = %v", err)
	}
	if w.Slot != 1 || len(all) != 3 {
		t.Fatalf("winner/all = %+v/%d, 期望 Slot 1 与 3 个候选", w, len(all))
	}
	select {
	case v := <-violations:
		t.Fatal(v)
	default:
	}

	mu.Lock()
	defer mu.Unlock()
	if len(rows) != 2 {
		t.Fatalf("回灌记录 = %+v, 期望只回灌胜者的 2 条", rows)
	}
	if rows[0].kind != "candidate" || rows[0].payload != 1 {
		t.Fatalf("rows[0] = %+v, 期望 candidate/1", rows[0])
	}
	if rows[1].kind != "detail" || rows[1].payload != "s1" {
		t.Fatalf("rows[1] = %+v, 期望 detail/s1", rows[1])
	}
}

func TestRaceSelectorAggregateBudgetCap(t *testing.T) {
	total := Budget{
		MaxTurns:           12,
		MaxPromptTokens:    30,
		MaxTotalTokens:     90,
		MaxCostUSD:         9,
		DeclaredTurnTokens: 900,
	}
	// sliceForChild 先每项减半，累计字段再按 3 个候选分摊。
	want := Budget{
		MaxTurns:           6,
		MaxPromptTokens:    15,
		MaxTotalTokens:     15,
		MaxCostUSD:         1.5,
		DeclaredTurnTokens: 150,
	}
	factory := &raceChildFactory{}
	sel := RaceSelector{N: 3, Child: factory.child, MaxTotal: total}
	if _, _, err := sel.Select(context.Background(), Options{}, "p"); err != nil {
		t.Fatalf("Select() err = %v", err)
	}
	if len(factory.opts) != 3 {
		t.Fatalf("Child 调用次数 = %d, 期望 3", len(factory.opts))
	}
	for i, opts := range factory.opts {
		if opts.Budget != want {
			t.Fatalf("候选 %d 预算 = %+v, 期望 %+v", i, opts.Budget, want)
		}
	}
	if got := want.MaxCostUSD * 3; got > total.MaxCostUSD {
		t.Fatalf("候选花费上限之和 %v 超过聚合上限 %v", got, total.MaxCostUSD)
	}
}

func TestRaceSelectorDegeneratesWhenNLeOne(t *testing.T) {
	for _, n := range []int{-3, 0, 1} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			budget := Budget{MaxTurns: 7, MaxPromptTokens: 11, MaxCostUSD: 3, MaxTotalTokens: 100}
			factory := &raceChildFactory{}
			factory.onBuild = func(c *raceTestChild) {
				c.result = Result{Text: "唯一答案"}
			}
			sel := RaceSelector{N: n, Child: factory.child}
			w, all, err := sel.Select(context.Background(), Options{Budget: budget}, "p")
			if err != nil {
				t.Fatalf("Select() err = %v", err)
			}
			if len(factory.opts) != 1 {
				t.Fatalf("Child 调用次数 = %d, 期望 1（单次退化）", len(factory.opts))
			}
			if factory.opts[0].Budget != budget {
				t.Fatalf("候选预算 = %+v, 期望原样沿用 %+v", factory.opts[0].Budget, budget)
			}
			if len(all) != 1 || all[0].Slot != 0 || all[0].ID != "candidate-0" {
				t.Fatalf("all = %+v, 期望单个 Slot 0 候选", all)
			}
			if w.Slot != 0 || w.Result.Text != "唯一答案" {
				t.Fatalf("winner = %+v, 期望唯一候选胜出", w)
			}
		})
	}
}

func TestRaceSelectorDefaultRubricCJK(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		withErr bool
		want    int
	}{
		{name: "中文非空答案", text: "已找到答案：北京", want: 1},
		{name: "中文答案两侧空白", text: "  中文答案  ", want: 1},
		{name: "中文中的失败词不算 FAIL", text: "任务失败，需重试", want: 1},
		{name: "大写 FAIL", text: "结果 FAIL", want: 0},
		{name: "小写 fail", text: "结果 fail", want: 0},
		{name: "空文本", text: "", want: 0},
		{name: "纯空白", text: " \t\n ", want: 0},
		{name: "带错误", text: "有效答案", withErr: true, want: 0},
	}
	r := raceDefaultRubric{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := RaceCandidate{Result: Result{Text: tc.text}}
			if tc.withErr {
				c.Err = errors.New("boom")
			}
			got, reason := r.Score(c)
			if got != tc.want {
				t.Fatalf("Score() = %d (%s), 期望 %d", got, reason, tc.want)
			}
			again, againReason := r.Score(c)
			if again != got || againReason != reason {
				t.Fatalf("Score() 非确定性: (%d,%s) -> (%d,%s)", got, reason, again, againReason)
			}
		})
	}
}

func TestRaceSelectorCJKWinner(t *testing.T) {
	factory := &raceChildFactory{}
	factory.onBuild = func(c *raceTestChild) {
		switch c.slot {
		case 0:
			c.result = Result{Text: "候选零：失败"}
		case 1:
			c.result = Result{Text: "候选一：FAIL"}
		default:
			c.result = Result{Text: "候选二：已完成（中文）"}
		}
	}
	sel := RaceSelector{N: 3, Child: factory.child}
	w, _, err := sel.Select(context.Background(), Options{}, "请分析")
	if err != nil {
		t.Fatalf("Select() err = %v", err)
	}
	// 默认评分下只有 Slot 0 与 Slot 2 得 1 分，同分按 Slot 升序取 Slot 0。
	if w.Slot != 0 || w.Result.Text != "候选零：失败" {
		t.Fatalf("winner = %+v, 期望中文非空且不含 FAIL 的 Slot 0", w)
	}
}

func TestRaceSelectorConcurrencyLimited(t *testing.T) {
	const n = 6
	started := make(chan struct{}, n)
	gate := make(chan struct{})
	factory := &raceChildFactory{}
	factory.onBuild = func(c *raceTestChild) {
		c.result = Result{Text: fmt.Sprintf("r%d", c.slot)}
		c.onRun = func(_ int, _ Options) {
			started <- struct{}{}
			<-gate
		}
	}
	sel := RaceSelector{N: n, Child: factory.child}
	done := make(chan struct{})
	go func() {
		_, _, _ = sel.Select(context.Background(), Options{MaxParallel: 2}, "p")
		close(done)
	}()

	<-started
	<-started
	select {
	case <-started:
		t.Fatal("MaxParallel=2 时第三个候选不应启动")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Select() 未在规定时间内完成")
	}
}

func TestRaceSelectorKeepsFailedCandidates(t *testing.T) {
	boom := errors.New("boom")
	factory := &raceChildFactory{}
	factory.onBuild = func(c *raceTestChild) {
		c.result = Result{Text: fmt.Sprintf("answer-%d", c.slot)}
		if c.slot == 1 {
			c.err = boom
		}
	}
	sel := RaceSelector{N: 3, Child: factory.child}
	w, all, err := sel.Select(context.Background(), Options{}, "p")
	if err != nil {
		t.Fatalf("胜者应成功, err = %v", err)
	}
	if w.Slot != 0 {
		t.Fatalf("winner.Slot = %d, 期望 0", w.Slot)
	}
	if !errors.Is(all[1].Err, boom) {
		t.Fatalf("all[1].Err = %v, 期望保留失败候选的错误", all[1].Err)
	}
}
