package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// guardSplitShellWords 把一行渲染后的命令行按 POSIX shell 的字段拆分规则还原成
// 参数列表：处理空白分词、单引号包裹以及引号外的反斜杠转义。它只覆盖
// quoteShellValue 实际会产生的引用形态（安全词原样、危险词单引号包裹、
// 内嵌单引号改写为 '\”），用于验证渲染结果喂给 shell 后能无损还原原值。
// 仅用于测试，不追求完整 shell 语法。
func guardSplitShellWords(t *testing.T, line string) []string {
	t.Helper()
	var words []string
	var cur strings.Builder
	hasWord := false
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			} else {
				cur.WriteByte(c)
			}
			hasWord = true
		case c == '\'':
			inQuote = true
			hasWord = true
		case c == '\\':
			// quoteShellValue 产生的反斜杠只出现在引号外，用于转义紧随其后的单引号。
			i++
			if i >= len(line) {
				t.Fatalf("结尾出现孤立反斜杠: %q", line)
			}
			cur.WriteByte(line[i])
			hasWord = true
		case c == ' ' || c == '\t':
			if hasWord {
				words = append(words, cur.String())
				cur.Reset()
				hasWord = false
			}
		default:
			cur.WriteByte(c)
			hasWord = true
		}
	}
	if inQuote {
		t.Fatalf("引号未闭合: %q", line)
	}
	if hasWord {
		words = append(words, cur.String())
	}
	return words
}

// TestGuardRenderCommandShellInjection 验证注入防护：值含空格、分号、美元符、
// 反引号、单引号、命令替换或 CJK 时被单引号安全包裹，内嵌单引号改写为 '\”。
// 同时用 guardSplitShellWords 证明渲染结果喂给 shell 后可无损还原为原始 argv。
func TestGuardRenderCommandShellInjection(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string // {{x}} 被替换后的精确文本
	}{
		{"safe word untouched", "foo.txt", "foo.txt"},
		{"empty quoted", "", "''"},
		{"space", "hello world", "'hello world'"},
		{"semicolon", "a;rm -rf /", "'a;rm -rf /'"},
		{"dollar", "$HOME", "'$HOME'"},
		{"command substitution", "$(id)", "'$(id)'"},
		{"backtick", "`whoami`", "'`whoami`'"},
		{"single quote", "it's", "'it'\\''s'"},
		{"multiple single quotes", "x'y'z", "'x'\\''y'\\''z'"},
		{"cjk", "世界", "'世界'"},
		{"cjk with space", "你好 世界", "'你好 世界'"},
		{"chain operators", "a && b || c", "'a && b || c'"},
		{"newline-ish literal", "a\tb", "'a\tb'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderCommand(CommandTemplate{
				Template: "echo {{x}}",
				Vars:     map[string]string{"x": tc.value},
			})
			if err != nil {
				t.Fatalf("RenderCommand(%q): %v", tc.value, err)
			}
			want := "echo " + tc.want
			if got != want {
				t.Fatalf("渲染结果 = %q, want %q", got, want)
			}
			// 危险值必须被引用；安全词保持原样。
			if tc.want != tc.value && !strings.HasPrefix(tc.want, "'") {
				t.Fatalf("危险值未被单引号包裹: %q", tc.want)
			}
			words := guardSplitShellWords(t, got)
			wantWords := []string{"echo", tc.value}
			if !reflect.DeepEqual(words, wantWords) {
				t.Fatalf("shell 还原 argv = %#v, want %#v（原始值 %q）", words, wantWords, tc.value)
			}
		})
	}
}

// TestGuardRenderCommandMultiArgInjection 验证一条命令中的多个危险参数各自独立
// 引用，不会因某个值里含空格或引号而破坏参数边界。
func TestGuardRenderCommandMultiArgInjection(t *testing.T) {
	got, err := RenderCommand(CommandTemplate{
		Template: "printf %s {{a}} {{b}} {{c}} {{d}}",
		Vars: map[string]string{
			"a": "a b",
			"b": "x'y",
			"c": "$HOME",
			"d": "中文 值",
		},
	})
	if err != nil {
		t.Fatalf("RenderCommand: %v", err)
	}
	wantWords := []string{"printf", "%s", "a b", "x'y", "$HOME", "中文 值"}
	if words := guardSplitShellWords(t, got); !reflect.DeepEqual(words, wantWords) {
		t.Fatalf("多参数 shell 还原 = %#v, want %#v（渲染为 %q）", words, wantWords, got)
	}
}

// TestGuardRenderCommandMissingVarSentinel 验证缺失变量哨兵：错误可用 errors.Is
// 精确匹配 ErrMissingVar 且点名变量；提供默认值可避免错误，默认值同样被引用；
// 注入的 {{...}} 值不会被重扫成新标记；Vars 始终不被修改。
func TestGuardRenderCommandMissingVarSentinel(t *testing.T) {
	t.Run("missing without default", func(t *testing.T) {
		got, err := RenderCommand(CommandTemplate{
			Template: "echo {{first}} {{second}}",
			Vars:     map[string]string{"first": "ok"},
		})
		if !errors.Is(err, ErrMissingVar) {
			t.Fatalf("error = %v, want ErrMissingVar", err)
		}
		if got != "" {
			t.Fatalf("出错时返回了部分渲染结果 %q, want 空串", got)
		}
		if !strings.Contains(err.Error(), "second") {
			t.Fatalf("错误信息未点名缺失变量: %v", err)
		}
	})

	t.Run("default avoids sentinel and is quoted", func(t *testing.T) {
		got, err := RenderCommand(CommandTemplate{
			Template: "echo {{name:-hello world}}",
			Vars:     nil,
		})
		if err != nil {
			t.Fatalf("有默认值不应报错: %v", err)
		}
		if got != "echo 'hello world'" {
			t.Fatalf("渲染结果 = %q", got)
		}
	})

	t.Run("injected placeholder not rescanned", func(t *testing.T) {
		got, err := RenderCommand(CommandTemplate{
			Template: "echo {{x}}",
			Vars:     map[string]string{"x": "{{missing}}; $(id)"},
		})
		if err != nil {
			t.Fatalf("注入的标记不应被当作新占位符处理: %v", err)
		}
		if got != "echo '{{missing}}; $(id)'" {
			t.Fatalf("渲染结果 = %q", got)
		}
	})

	t.Run("vars untouched", func(t *testing.T) {
		vars := map[string]string{"x": "a b", "unused": "keep"}
		tmpl := CommandTemplate{Template: "echo {{x}} {{y:-d}}", Vars: vars}
		if _, err := RenderCommand(tmpl); err != nil {
			t.Fatalf("RenderCommand: %v", err)
		}
		want := map[string]string{"x": "a b", "unused": "keep"}
		if !reflect.DeepEqual(vars, want) {
			t.Fatalf("Vars 被修改: got %#v, want %#v", vars, want)
		}
	})
}

// TestGuardTemplateVarsOrderDedup 验证 TemplateVars 按首次出现顺序去重，默认值
// 只报告变量名，空名/未终止标记被忽略，CJK 变量名原样保留。
func TestGuardTemplateVarsOrderDedup(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want []string
	}{
		{"first-seen order with defaults", "{{b}} {{a:-1}} {{b:-2}} {{c}} {{a}}", []string{"b", "a", "c"}},
		{"cjk names", "{{名字:-世界}} {{步骤}} {{名字}}", []string{"名字", "步骤"}},
		{"ignores invalid markers", "{{}} {{ ok }} {{半截", []string{"ok"}},
		{"duplicate default name", "{{url:-http://x}} {{url:-ftp://y}}", []string{"url"}},
		{"no placeholders", "plain text", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TemplateVars(tc.s)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("TemplateVars(%q) = %#v, want %#v", tc.s, got, tc.want)
			}
			// 重复调用必须稳定。
			if again := TemplateVars(tc.s); !reflect.DeepEqual(again, tc.want) {
				t.Fatalf("重复调用 TemplateVars(%q) = %#v, want %#v", tc.s, again, tc.want)
			}
		})
	}
}

// TestGuardPlanTraceConcurrentCJK 在多个 goroutine 中用 CJK 步骤名与相位并发
// Mark/Snapshot/StepsInPhase，配合 -race 检查锁保护。每个步骤名唯一，因此最终
// 条目数精确等于 Mark 次数，且相位去重集合完整、无重复。
func TestGuardPlanTraceConcurrentCJK(t *testing.T) {
	tr := NewPlanTrace()
	const (
		goroutines = 8
		perG       = 150
	)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				tr.Mark(fmt.Sprintf("步骤-%d-%d", id, i), "就绪", int64(i))
				_ = tr.Snapshot()
				_ = tr.StepsInPhase("就绪")
			}
		}(g)
	}
	wg.Wait()

	wantCount := goroutines * perG
	snap := tr.Snapshot()
	if len(snap) != wantCount {
		t.Fatalf("len(Snapshot()) = %d, want %d", len(snap), wantCount)
	}
	seen := make(map[string]bool, wantCount)
	for _, e := range snap {
		if e.Phase != "就绪" || seen[e.Step] {
			t.Fatalf("意外或重复的条目: %#v", e)
		}
		seen[e.Step] = true
	}
	steps := tr.StepsInPhase("就绪")
	if len(steps) != wantCount {
		t.Fatalf("len(StepsInPhase(就绪)) = %d, want %d", len(steps), wantCount)
	}
	if got := tr.StepsInPhase("运行中"); got != nil {
		t.Fatalf("StepsInPhase(运行中) = %#v, want nil", got)
	}
}

// TestGuardPlanTraceFirstSeenOrderStable 固定 StepsInPhase 的首次出现顺序去重：
// 同一步骤重复记录相同相位只保留第一次位置，不同步骤保持稳定顺序；CJK 相位
// 同样生效；修改返回切片不污染轨迹。
func TestGuardPlanTraceFirstSeenOrderStable(t *testing.T) {
	tr := NewPlanTrace()
	marks := []TraceEntry{
		{Step: "扫描", Phase: "就绪", At: 1},
		{Step: "复核", Phase: "就绪", At: 2},
		{Step: "扫描", Phase: "就绪", At: 3}, // 重复，应被去重
		{Step: "汇总", Phase: "就绪", At: 4},
		{Step: "扫描", Phase: "完成", At: 5},
		{Step: "汇总", Phase: "就绪", At: 6}, // 重复，应被去重
	}
	for _, e := range marks {
		tr.Mark(e.Step, e.Phase, e.At)
	}

	if got := tr.Snapshot(); !reflect.DeepEqual(got, marks) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, marks)
	}
	if got := tr.StepsInPhase("就绪"); !reflect.DeepEqual(got, []string{"扫描", "复核", "汇总"}) {
		t.Fatalf("StepsInPhase(就绪) = %#v, want [扫描 复核 汇总]", got)
	}
	if got := tr.StepsInPhase("完成"); !reflect.DeepEqual(got, []string{"扫描"}) {
		t.Fatalf("StepsInPhase(完成) = %#v, want [扫描]", got)
	}

	mutated := tr.StepsInPhase("就绪")
	mutated[0] = "篡改"
	mutated = append(mutated, "注入")
	if again := tr.StepsInPhase("就绪"); !reflect.DeepEqual(again, []string{"扫描", "复核", "汇总"}) {
		t.Fatalf("修改返回切片污染了轨迹: %#v", again)
	}
}

// guardCloneStepPlan 深复制 StepPlan 并保留 nil 与空值的区别，用于验证纯函数性。
func guardCloneStepPlan(p StepPlan) StepPlan {
	out := StepPlan{}
	if p.Order != nil {
		out.Order = append([]string{}, p.Order...)
	}
	if p.ReadyNow != nil {
		out.ReadyNow = append([]string{}, p.ReadyNow...)
	}
	if p.Blocked != nil {
		out.Blocked = make(map[string][]string, len(p.Blocked))
		for name, deps := range p.Blocked {
			if deps != nil {
				out.Blocked[name] = append([]string{}, deps...)
			} else {
				out.Blocked[name] = nil
			}
		}
	}
	return out
}

// TestGuardBuildStepPlanReadyInvariants 验证就绪计划的核心不变量：完成一步后
// 下游变就绪、Blocked 正确收缩、ReadyNow 按声明顺序、Order 来自 SimulateRun；
// 并覆盖环时 Order 为空与 CJK 名称。
func TestGuardBuildStepPlanReadyInvariants(t *testing.T) {
	linear := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.a.output}}"},
		{Name: "c", Prompt: "{{steps.b.output}}"},
	}
	cjk := []Step{
		{Name: "扫描", Prompt: "列出改动"},
		{Name: "复核", Prompt: "复核 {{steps.扫描.output}}"},
		{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
	}

	t.Run("linear downstream becomes ready", func(t *testing.T) {
		plan := BuildStepPlan(linear, nil)
		wantOrder, _ := SimulateRun(linear)
		if !reflect.DeepEqual(plan.Order, wantOrder) {
			t.Fatalf("Order = %#v, want SimulateRun %#v", plan.Order, wantOrder)
		}
		if !reflect.DeepEqual(plan.ReadyNow, []string{"a"}) {
			t.Fatalf("初始 ReadyNow = %#v, want [a]", plan.ReadyNow)
		}
		if !reflect.DeepEqual(plan.Blocked, map[string][]string{"b": {"a"}, "c": {"b"}}) {
			t.Fatalf("初始 Blocked = %#v", plan.Blocked)
		}

		next := AdvancePlan(plan, "a")
		if !reflect.DeepEqual(next.ReadyNow, []string{"b"}) {
			t.Fatalf("完成 a 后 ReadyNow = %#v, want [b]", next.ReadyNow)
		}
		if !reflect.DeepEqual(next.Blocked, map[string][]string{"c": {"b"}}) {
			t.Fatalf("完成 a 后 Blocked = %#v", next.Blocked)
		}
		// Order 不因推进而改变。
		if !reflect.DeepEqual(next.Order, plan.Order) {
			t.Fatalf("推进后 Order = %#v, want %#v", next.Order, plan.Order)
		}
	})

	t.Run("cycle yields empty order", func(t *testing.T) {
		cycle := []Step{
			{Name: "a", Prompt: "{{steps.b.output}}"},
			{Name: "b", Prompt: "{{steps.a.output}}"},
		}
		plan := BuildStepPlan(cycle, nil)
		if plan.Order != nil {
			t.Fatalf("环图 Order = %#v, want nil", plan.Order)
		}
		if len(plan.ReadyNow) != 0 {
			t.Fatalf("环图 ReadyNow = %#v, want 空", plan.ReadyNow)
		}
	})

	t.Run("cjk partial completion", func(t *testing.T) {
		plan := BuildStepPlan(cjk, map[string]bool{"扫描": true})
		if !reflect.DeepEqual(plan.ReadyNow, []string{"复核"}) {
			t.Fatalf("CJK ReadyNow = %#v, want [复核]", plan.ReadyNow)
		}
		// StepDependencyMap 依赖名按字典序：复核 < 汇总（U+590D < U+6C47）。
		if !reflect.DeepEqual(plan.Blocked, map[string][]string{"汇总": {"复核"}}) {
			t.Fatalf("CJK Blocked = %#v", plan.Blocked)
		}
		next := AdvancePlan(plan, "复核")
		if !reflect.DeepEqual(next.ReadyNow, []string{"汇总"}) {
			t.Fatalf("完成复核后 ReadyNow = %#v, want [汇总]", next.ReadyNow)
		}
		if len(next.Blocked) != 0 {
			t.Fatalf("完成复核后 Blocked = %#v, want 空", next.Blocked)
		}
	})

	t.Run("pure functions", func(t *testing.T) {
		completed := map[string]bool{"a": true}
		stepsSnapshot := append([]Step{}, linear...)
		completedSnapshot := map[string]bool{"a": true}
		plan := BuildStepPlan(linear, completed)
		snapshot := guardCloneStepPlan(plan)

		if !reflect.DeepEqual(linear, stepsSnapshot) || !reflect.DeepEqual(completed, completedSnapshot) {
			t.Fatalf("BuildStepPlan 修改了入参: steps=%+v completed=%v", linear, completed)
		}
		got := AdvancePlan(plan, "b")
		want := guardCloneStepPlan(got)
		if !reflect.DeepEqual(plan, snapshot) {
			t.Fatalf("AdvancePlan 修改了入参 plan: got %#v, want %#v", plan, snapshot)
		}
		// 修改返回值不污染入参。
		if len(got.Order) > 0 {
			got.Order[0] = "篡改"
		}
		if len(got.ReadyNow) > 0 {
			got.ReadyNow[0] = "篡改"
		}
		for name, deps := range got.Blocked {
			if len(deps) > 0 {
				deps[0] = "篡改"
			}
			got.Blocked[name] = deps
		}
		if again := AdvancePlan(plan, "b"); !reflect.DeepEqual(again, want) {
			t.Fatalf("修改返回值影响了后续调用: got %#v, want %#v", again, want)
		}
	})
}

// TestGuardAdvancePlanOrderIsNeverRecomputed 用一个人为构造、与图结构无关的 Order
// 直接喂给 AdvancePlan，验证它只是原样保留、绝不重算；同时确认环图推进后 Order
// 仍为空。
func TestGuardAdvancePlanOrderIsNeverRecomputed(t *testing.T) {
	plan := StepPlan{
		Order:    []string{"fabricated-1", "fabricated-2"},
		ReadyNow: []string{"a"},
		Blocked:  map[string][]string{"b": {"a"}},
	}
	snapshot := guardCloneStepPlan(plan)
	got := AdvancePlan(plan, "a")

	if !reflect.DeepEqual(got.Order, []string{"fabricated-1", "fabricated-2"}) {
		t.Fatalf("Order 被重算: got %#v", got.Order)
	}
	if !reflect.DeepEqual(got.ReadyNow, []string{"b"}) || len(got.Blocked) != 0 {
		t.Fatalf("推进结果不正确: ReadyNow=%#v Blocked=%#v", got.ReadyNow, got.Blocked)
	}
	if !reflect.DeepEqual(plan, snapshot) {
		t.Fatalf("AdvancePlan 修改了入参: got %#v, want %#v", plan, snapshot)
	}

	cyclePlan := BuildStepPlan([]Step{
		{Name: "甲", Prompt: "{{steps.乙.output}}"},
		{Name: "乙", Prompt: "{{steps.甲.output}}"},
	}, nil)
	if cyclePlan.Order != nil {
		t.Fatalf("环图 Order = %#v, want nil", cyclePlan.Order)
	}
	advanced := AdvancePlan(cyclePlan, "甲")
	if advanced.Order != nil {
		t.Fatalf("环图推进后 Order = %#v, want nil", advanced.Order)
	}
}

// TestGuardAdvancePlanConvergesToBuildStepPlan 端到端验证：按拓扑顺序逐步完成
// 每一步，AdvancePlan 的就绪结果始终与使用累积完成集重新 BuildStepPlan 一致，
// 覆盖线性、菱形与 CJK 三种图。
func TestGuardAdvancePlanConvergesToBuildStepPlan(t *testing.T) {
	graphs := [][]Step{
		{
			{Name: "a", Prompt: "root"},
			{Name: "b", Prompt: "{{steps.a.output}}"},
			{Name: "c", Prompt: "{{steps.b.output}}"},
		},
		{
			{Name: "a", Prompt: "root"},
			{Name: "b", Prompt: "{{steps.a.output}}"},
			{Name: "c", Prompt: "{{steps.a.output}}"},
			{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
		},
		{
			{Name: "扫描", Prompt: "列出改动"},
			{Name: "复核", Prompt: "{{steps.扫描.output}}"},
			{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
		},
	}

	for gi, steps := range graphs {
		order, err := SimulateRun(steps)
		if err != nil {
			t.Fatalf("图 %d: SimulateRun: %v", gi, err)
		}
		completed := map[string]bool{}
		plan := BuildStepPlan(steps, completed)
		for _, name := range order {
			plan = AdvancePlan(plan, name)
			completed[name] = true
			want := BuildStepPlan(steps, completed)
			if !reflect.DeepEqual(plan.ReadyNow, want.ReadyNow) {
				t.Fatalf("图 %d 完成 %q: ReadyNow = %#v, want %#v", gi, name, plan.ReadyNow, want.ReadyNow)
			}
			if !reflect.DeepEqual(plan.Blocked, want.Blocked) {
				t.Fatalf("图 %d 完成 %q: Blocked = %#v, want %#v", gi, name, plan.Blocked, want.Blocked)
			}
			if !reflect.DeepEqual(plan.Order, want.Order) {
				t.Fatalf("图 %d 完成 %q: Order = %#v, want %#v", gi, name, plan.Order, want.Order)
			}
		}
		if len(plan.ReadyNow) != 0 || len(plan.Blocked) != 0 {
			t.Fatalf("图 %d 全部完成后应无就绪与阻塞: ReadyNow=%#v Blocked=%#v", gi, plan.ReadyNow, plan.Blocked)
		}
	}
}

// TestGuardReadyStepsDeclarationOrderStable 验证 ReadySteps 始终按声明顺序返回
// 就绪步骤（而非字典序），重复调用稳定，返回切片与入参零共享，并覆盖 CJK 名称。
func TestGuardReadyStepsDeclarationOrderStable(t *testing.T) {
	steps := []Step{
		{Name: "z", Prompt: "root"},
		{Name: "m", Prompt: "{{steps.z.output}}"},
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.z.output}}"},
	}
	stepsSnapshot := append([]Step{}, steps...)

	wantInitially := []string{"z", "a"}
	for i := 0; i < 5; i++ {
		if got := ReadySteps(steps, nil); !reflect.DeepEqual(got, wantInitially) {
			t.Fatalf("调用 %d: ReadySteps(nil) = %#v, want %#v", i, got, wantInitially)
		}
	}
	wantAfter := []string{"m", "a", "b"}
	completed := map[string]bool{"z": true}
	if got := ReadySteps(steps, completed); !reflect.DeepEqual(got, wantAfter) {
		t.Fatalf("ReadySteps(z=true) = %#v, want %#v", got, wantAfter)
	}

	// 修改返回切片不应影响入参或后续调用。
	got := ReadySteps(steps, completed)
	got[0] = "篡改"
	got = append(got, "注入")
	if again := ReadySteps(steps, completed); !reflect.DeepEqual(again, wantAfter) {
		t.Fatalf("修改返回值影响后续调用: %#v", again)
	}
	if !reflect.DeepEqual(steps, stepsSnapshot) {
		t.Fatalf("ReadySteps 修改了入参: %+v", steps)
	}
	if !reflect.DeepEqual(completed, map[string]bool{"z": true}) {
		t.Fatalf("ReadySteps 修改了 completed: %v", completed)
	}

	// CJK 名称同样按声明顺序。
	cjk := []Step{
		{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
		{Name: "扫描", Prompt: "列出改动"},
		{Name: "复核", Prompt: "{{steps.扫描.output}}"},
	}
	if want := []string{"扫描"}; !reflect.DeepEqual(ReadySteps(cjk, nil), want) {
		t.Fatalf("CJK ReadySteps(nil) = %#v, want %#v", ReadySteps(cjk, nil), want)
	}
	// 扫描已完成后从就绪集合中剔除，仅剩复核；汇总仍被未完成的复核阻塞。
	if want := []string{"复核"}; !reflect.DeepEqual(ReadySteps(cjk, map[string]bool{"扫描": true}), want) {
		t.Fatalf("CJK ReadySteps(扫描=true) = %#v, want %#v", ReadySteps(cjk, map[string]bool{"扫描": true}), want)
	}
}

// TestGuardIsStepReadyBranches 逐分支验证 IsStepReady：nil/空完成集、无依赖、
// 单个依赖完成/缺失/显式 false、部分完成、全部完成、仅 Args 中的依赖、CJK 依赖、
// 畸形引用忽略、重复依赖与 Prompt+Args 混合依赖。
func TestGuardIsStepReadyBranches(t *testing.T) {
	cases := []struct {
		name      string
		step      Step
		completed map[string]bool
		want      bool
	}{
		{"no deps nil completed", Step{Name: "a"}, nil, true},
		{"no deps empty completed", Step{Name: "a"}, map[string]bool{}, true},
		{"single dep done", Step{Name: "b", Prompt: "{{steps.a.output}}"}, map[string]bool{"a": true}, true},
		{"single dep missing", Step{Name: "b", Prompt: "{{steps.a.output}}"}, nil, false},
		{"single dep false", Step{Name: "b", Prompt: "{{steps.a.output}}"}, map[string]bool{"a": false}, false},
		{"partial deps", Step{Name: "c", Prompt: "{{steps.a.output}} {{steps.b.output}}"}, map[string]bool{"a": true}, false},
		{"all deps with extras", Step{Name: "c", Prompt: "{{steps.a.output}} {{steps.b.output}}"}, map[string]bool{"a": true, "b": true, "extra": true}, true},
		{"args-only dep", Step{Name: "d", Tool: "write", Args: `{"x":"{{steps.a.output}}"}`}, map[string]bool{"a": true}, true},
		{"mixed prompt and args deps", Step{Name: "e", Prompt: "{{steps.a.output}}", Args: `{"x":"{{steps.b.output}}"}`}, map[string]bool{"a": true}, false},
		{"mixed prompt and args all done", Step{Name: "e", Prompt: "{{steps.a.output}}", Args: `{"x":"{{steps.b.output}}"}`}, map[string]bool{"a": true, "b": true}, true},
		{"cjk dep done", Step{Name: "汇总", Prompt: "{{steps.扫描.output}}"}, map[string]bool{"扫描": true}, true},
		{"cjk dep missing", Step{Name: "汇总", Prompt: "{{steps.扫描.output}}"}, map[string]bool{"扫描": false}, false},
		{"malformed refs ignored", Step{Name: "a", Prompt: "{{steps..output}} {{steps.b}} {{input}}"}, nil, true},
		{"duplicate dep needs one completion", Step{Name: "b", Prompt: "{{steps.a.output}} {{steps.a.output}}"}, map[string]bool{"a": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStepReady(tc.step, tc.completed); got != tc.want {
				t.Fatalf("IsStepReady(%+v, %v) = %v, want %v", tc.step, tc.completed, got, tc.want)
			}
		})
	}
}
