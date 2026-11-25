package llm_test

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"example.com/argentgo/internal/llm"
)

// 本文件是对 R13 SubstBudget 的属性守护，不触碰任何生产逻辑。
// SubstBudget 是一个纯计数、并发安全的递归深度预算器；这里通过 reflect 读取其
// 未导出字段，与一份独立参考模型逐字段对拍，并在并发压测中用原子计数精确核对
// "恰好 maxDepth 次成功"这一核心承诺。
//
// 所有新增辅助统一以 guard 前缀命名，避免与既有测试符号冲突。

// ---- reflect 读取内部记账字段（仅测试使用） ----

// guardReflectInt 读取 SubstBudget 的未导出 int 字段，供对拍使用。
func guardReflectInt(t *testing.T, b *llm.SubstBudget, name string) int {
	t.Helper()
	f := reflect.ValueOf(b).Elem().FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.Int {
		t.Fatalf("读取 SubstBudget.%s 失败: valid=%v kind=%v", name, f.IsValid(), f.Kind())
	}
	return int(f.Int())
}

// guardReflectBool 读取 SubstBudget 的未导出 bool 字段。
func guardReflectBool(t *testing.T, b *llm.SubstBudget, name string) bool {
	t.Helper()
	f := reflect.ValueOf(b).Elem().FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.Bool {
		t.Fatalf("读取 SubstBudget.%s 失败: valid=%v kind=%v", name, f.IsValid(), f.Kind())
	}
	return f.Bool()
}

// guardDepth 返回当前已进入未退出的嵌套层数。
func guardDepth(t *testing.T, b *llm.SubstBudget) int {
	return guardReflectInt(t, b, "depth")
}

// guardCalls 返回 Enter 被调用的总次数。
func guardCalls(t *testing.T, b *llm.SubstBudget) int {
	return guardReflectInt(t, b, "calls")
}

// guardMaxDepth 返回生效的嵌套上限。
func guardMaxDepth(t *testing.T, b *llm.SubstBudget) int {
	return guardReflectInt(t, b, "maxDepth")
}

// guardExhaustedFlag 返回是否曾触顶的 sticky 标志（直接读字段，绕开公开 API）。
func guardExhaustedFlag(t *testing.T, b *llm.SubstBudget) bool {
	return guardReflectBool(t, b, "exhausted")
}

// guardState 是 SubstBudget 四个记账字段的一次快照，用值相等做逐字段比较。
type guardState struct {
	depth     int
	maxDepth  int
	calls     int
	exhausted bool
}

// guardStateOf 抓取一次完整快照。只在无并发写入的时点调用（如 wg.Wait 之后）。
func guardStateOf(t *testing.T, b *llm.SubstBudget) guardState {
	t.Helper()
	return guardState{
		depth:     guardDepth(t, b),
		maxDepth:  guardMaxDepth(t, b),
		calls:     guardCalls(t, b),
		exhausted: guardExhaustedFlag(t, b),
	}
}

// ---- 独立参考模型（不复用任何生产代码） ----

// guardModel 严格按 SubstBudget 注释里的契约实现记账：
// 成功的 Enter 与失败的 Enter 都 calls++、depth++；Exit 仅在 depth>0 时递减；
// 一旦超限 exhausted 置位且不再清空。
type guardModel struct {
	maxDepth  int
	depth     int
	calls     int
	exhausted bool
}

func (m *guardModel) enter() bool {
	m.calls++
	m.depth++
	if m.depth > m.maxDepth {
		m.exhausted = true
		return false
	}
	return true
}

func (m *guardModel) exit() {
	if m.depth > 0 {
		m.depth--
	}
}

func (m *guardModel) state() guardState {
	return guardState{
		depth:     m.depth,
		maxDepth:  m.maxDepth,
		calls:     m.calls,
		exhausted: m.exhausted,
	}
}

// guardNextRand 是线性同余伪随机源，保证用例完全确定、可复现。
func guardNextRand(s *uint64) uint64 {
	*s = *s*6364136223846793005 + 1442695040888963407
	return *s
}

// guardAssertMatchesModel 对拍真实实现与参考模型的全部记账字段，并额外检查
// 公开观测 API 与内部标志一致、depth 从不为负。
func guardAssertMatchesModel(t *testing.T, b *llm.SubstBudget, m *guardModel, step int) {
	t.Helper()
	if got, want := guardStateOf(t, b), m.state(); got != want {
		t.Fatalf("第 %d 步状态偏离参考模型: got %+v, want %+v", step, got, want)
	}
	if d := guardDepth(t, b); d < 0 {
		t.Fatalf("第 %d 步 depth 为负: %d", step, d)
	}
	if got, want := b.Exhausted(), guardExhaustedFlag(t, b); got != want {
		t.Fatalf("第 %d 步 Exhausted()=%v, 内部 exhausted=%v", step, got, want)
	}
	if got, want := b.Calls(), guardCalls(t, b); got != want {
		t.Fatalf("第 %d 步 Calls()=%d, 内部 calls=%d", step, got, want)
	}
}

// TestSubstBudgetGuardReferenceModelNoExhaustion 在大上限下跑确定性随机 Enter/Exit
// 序列，每一步与独立参考计数逐字段对拍。depth 始终非负、calls 精确累加、
// 不误报 exhausted，从而验证配对记账的正确性。
func TestSubstBudgetGuardReferenceModelNoExhaustion(t *testing.T) {
	const (
		maxDepth = 1 << 20 // 足够大，整段随机漫步都不会触顶
		steps    = 6000
	)
	b := llm.NewSubstBudget(maxDepth)
	m := &guardModel{maxDepth: maxDepth}
	var seed uint64 = 0x9e3779b97f4a7c15

	for i := 0; i < steps; i++ {
		if guardNextRand(&seed)%2 == 0 {
			b.Exit()
			m.exit()
		} else if got, want := b.Enter(), m.enter(); got != want {
			t.Fatalf("第 %d 步 Enter=%v, want %v", i, got, want)
		}
		guardAssertMatchesModel(t, b, m, i)
	}
	if guardExhaustedFlag(t, b) {
		t.Fatal("大上限随机序列不应 exhausted")
	}
	// 独立参考计数与真实计数完全一致。
	if got, want := guardCalls(t, b), m.calls; got != want {
		t.Fatalf("Calls = %d, want %d（独立参考计数）", got, want)
	}
}

// TestSubstBudgetGuardReferenceModelExhaustionSticky 在小上限下连续进入至触顶，
// 再大量退出：前 maxDepth 次成功、其余被拒绝；depth 钳制到 0，而 exhausted
// 一旦置位保持 sticky。全程与参考模型逐步对拍。
func TestSubstBudgetGuardReferenceModelExhaustionSticky(t *testing.T) {
	const maxDepth = 3
	b := llm.NewSubstBudget(maxDepth)
	m := &guardModel{maxDepth: maxDepth}
	step := 0

	for i := 0; i < maxDepth+2; i++ {
		got, want := b.Enter(), m.enter()
		if got != want || got != (i < maxDepth) {
			t.Fatalf("第 %d 次 Enter=%v, want %v", i+1, got, i < maxDepth)
		}
		guardAssertMatchesModel(t, b, m, step)
		step++
	}
	if !guardExhaustedFlag(t, b) {
		t.Fatal("超限后内部 exhausted 应为 true")
	}

	peak := guardDepth(t, b) // 含被拒绝调用累加的残留深度
	for i := 0; i < 100; i++ {
		b.Exit()
		m.exit()
		guardAssertMatchesModel(t, b, m, step)
		step++
	}
	if got := guardDepth(t, b); got != 0 {
		t.Errorf("大量 Exit 后 depth = %d, want 0（曾达 %d）", got, peak)
	}
	if !guardExhaustedFlag(t, b) {
		t.Error("exhausted 应在大量 Exit 后仍保持 sticky")
	}
}

// TestSubstBudgetGuardConcurrentExactAccounting 用大上限并发 Enter/Exit：以原子计数
// 作为独立参考，验证 Calls 精确累加、depth 归零、上限未被改写且未误报触顶。
func TestSubstBudgetGuardConcurrentExactAccounting(t *testing.T) {
	const (
		goroutines = 8
		iterations = 400
		limit      = goroutines * iterations
	)
	b := llm.NewSubstBudget(limit)
	var ref atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if !b.Enter() {
					t.Error("大上限下 Enter 不应被拒绝")
					return
				}
				ref.Add(1)
				b.Exit()
			}
		}()
	}
	close(start)
	wg.Wait()

	want := int(ref.Load())
	if got := b.Calls(); got != want {
		t.Errorf("Calls() = %d, want %d（原子参考计数）", got, want)
	}
	if got := guardCalls(t, b); got != want {
		t.Errorf("内部 calls = %d, want %d", got, want)
	}
	if got := guardDepth(t, b); got != 0 {
		t.Errorf("全部配对退出后 depth = %d, want 0", got)
	}
	if got := guardMaxDepth(t, b); got != limit {
		t.Errorf("maxDepth = %d, want %d（不得被并发调用改写）", got, limit)
	}
	if guardExhaustedFlag(t, b) {
		t.Error("大上限下不应 exhausted")
	}
}

// TestSubstBudgetGuardConcurrentExactlyMaxDepth 是小上限并发拒绝压测：每轮让
// goroutines 个协程在同一栅门后同时 Enter，用原子成功计数断言恰好 maxDepth
// 次成功；失败的 Enter 仍计数并累加 depth，且 exhausted 置位。
func TestSubstBudgetGuardConcurrentExactlyMaxDepth(t *testing.T) {
	const (
		rounds     = 12
		maxDepth   = 9
		goroutines = 64
	)
	for r := 0; r < rounds; r++ {
		b := llm.NewSubstBudget(maxDepth)
		var successes, attempts atomic.Int64
		start := make(chan struct{})
		var wg sync.WaitGroup

		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				attempts.Add(1)
				if b.Enter() {
					successes.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if got := successes.Load(); got != maxDepth {
			t.Errorf("第 %d 轮: 成功 Enter = %d, want 恰好 %d", r, got, maxDepth)
		}
		if got := attempts.Load(); got != goroutines {
			t.Errorf("第 %d 轮: Enter 尝试 = %d, want %d", r, got, goroutines)
		}
		if got := b.Calls(); got != goroutines {
			t.Errorf("第 %d 轮: Calls() = %d, want %d", r, got, goroutines)
		}
		st := guardStateOf(t, b)
		if st.maxDepth != maxDepth {
			t.Errorf("第 %d 轮: maxDepth = %d, want %d", r, st.maxDepth, maxDepth)
		}
		if st.calls != goroutines {
			t.Errorf("第 %d 轮: 内部 calls = %d, want %d", r, st.calls, goroutines)
		}
		if st.depth != goroutines {
			t.Errorf("第 %d 轮: 内部 depth = %d, want %d（失败 Enter 也累加，且无人 Exit）", r, st.depth, goroutines)
		}
		if !st.exhausted {
			t.Errorf("第 %d 轮: 超限后 exhausted 应为 true", r)
		}
	}
}

// TestSubstBudgetGuardDefaultUpperBoundAndZeroValue 覆盖 <=0 采用默认上限 32，以及
// 零值 SubstBudget 可直接使用：首次 Enter 完成初始化，32 次成功，第 33 次被
// 拒绝并置 sticky；全部字段均通过 reflect 校验。
func TestSubstBudgetGuardDefaultUpperBoundAndZeroValue(t *testing.T) {
	cases := []struct {
		name string
		b    *llm.SubstBudget
	}{
		{name: "NewSubstBudget(0)", b: llm.NewSubstBudget(0)},
		{name: "NewSubstBudget(-7)", b: llm.NewSubstBudget(-7)},
		{name: "zero value struct", b: &llm.SubstBudget{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.b
			for i := 0; i < 32; i++ {
				if !b.Enter() {
					t.Fatalf("默认上限内第 %d 次 Enter 应成功", i+1)
				}
			}
			if st := guardStateOf(t, b); st.maxDepth != 32 || st.depth != 32 || st.calls != 32 || st.exhausted {
				t.Fatalf("默认上限内状态异常: %+v", st)
			}
			if b.Enter() {
				t.Fatal("第 33 次 Enter 应被拒绝")
			}
			if st := guardStateOf(t, b); st.calls != 33 || st.depth != 33 || !st.exhausted {
				t.Fatalf("第 33 次被拒绝后状态异常: %+v", st)
			}
			for i := 0; i < 50; i++ {
				b.Exit()
			}
			if st := guardStateOf(t, b); st.depth != 0 || !st.exhausted || st.maxDepth != 32 {
				t.Fatalf("大量退出后状态异常: %+v", st)
			}
		})
	}
}

// TestSubstBudgetGuardCJKIndependent 验证预算器是纯计数器：对 ASCII 与 CJK（含 emoji）
// 标签执行完全相同的操作序列，每一步的内部状态快照都必须一致，最终结果相同。
func TestSubstBudgetGuardCJKIndependent(t *testing.T) {
	ascii := llm.NewSubstBudget(3)
	cjk := llm.NewSubstBudget(3)
	labels := []string{"a", "b", "c", "命令", "替换", "展开", "一", "二", "三", "🚀"}

	for i, label := range labels {
		gotA, gotB := ascii.Enter(), cjk.Enter()
		if gotA != gotB {
			t.Fatalf("标签 %q: ASCII Enter=%v, CJK Enter=%v, 结果应一致", label, gotA, gotB)
		}
		_ = i
		if sa, sb := guardStateOf(t, ascii), guardStateOf(t, cjk); sa != sb {
			t.Fatalf("标签 %q: ASCII 与 CJK 状态分叉: %+v vs %+v", label, sa, sb)
		}
	}
	if !guardExhaustedFlag(t, cjk) {
		t.Error("两种内容都应因相同序列而 exhausted")
	}
}

// TestSubstBudgetGuardDeterministic 验证相同操作序列在两份新预算器上产生逐状态一致
// 的结果。操作序列由固定种子生成，因此用例可复现；覆盖触顶前后两段状态。
func TestSubstBudgetGuardDeterministic(t *testing.T) {
	var seed uint64 = 0xdeadbeefcafebabe
	ops := make([]bool, 2500)
	for i := range ops {
		ops[i] = guardNextRand(&seed)%3 != 0 // 约 2/3 Enter，确保最终触顶
	}

	run := func() []guardState {
		b := llm.NewSubstBudget(128)
		states := make([]guardState, 0, len(ops)+1)
		states = append(states, guardStateOf(t, b))
		for _, enter := range ops {
			if enter {
				b.Enter()
			} else {
				b.Exit()
			}
			states = append(states, guardStateOf(t, b))
		}
		return states
	}

	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("状态序列长度不同: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("第 %d 步状态不一致: %+v vs %+v", i, a[i], b[i])
		}
	}
	if !a[len(a)-1].exhausted {
		t.Error("该操作序列应在末尾触顶，以覆盖 exhausted 分支的确定性")
	}
}

// TestSubstBudgetGuardNoInputMutation 验证配置入参不被改写，且观测接口无副作用：
// 一是 maxDepth 在整段生命周期内保持构造值（零值初始化后固定为默认上限）；
// 二是反复调用 Exhausted/Calls 不改变任何记账字段。
func TestSubstBudgetGuardNoInputMutation(t *testing.T) {
	for _, limit := range []int{1, 7, 32, 1000} {
		b := llm.NewSubstBudget(limit)
		if before := guardMaxDepth(t, b); before != limit {
			t.Fatalf("limit=%d: 构造后 maxDepth=%d", limit, before)
		}
		for i := 0; i < 200; i++ {
			b.Enter()
			b.Exit()
		}
		if got := guardMaxDepth(t, b); got != limit {
			t.Errorf("limit=%d: 操作后 maxDepth 被改写为 %d", limit, got)
		}
	}

	z := &llm.SubstBudget{}
	z.Enter()
	want := guardMaxDepth(t, z)
	if want != 32 {
		t.Fatalf("零值初始化后 maxDepth=%d, want 32", want)
	}
	for i := 0; i < 100; i++ {
		z.Enter()
		z.Exit()
	}
	if got := guardMaxDepth(t, z); got != want {
		t.Errorf("零值预算器 maxDepth 被改写: %d -> %d", want, got)
	}

	b := llm.NewSubstBudget(5)
	for i := 0; i < 3; i++ {
		b.Enter()
	}
	snap := guardStateOf(t, b)
	for i := 0; i < 100; i++ {
		_ = b.Exhausted()
		_ = b.Calls()
	}
	if got := guardStateOf(t, b); got != snap {
		t.Errorf("观测接口改变了状态: before=%+v after=%+v", snap, got)
	}
}
