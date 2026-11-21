package workflow

import (
	"reflect"
	"testing"
)

// clonePlanStepsForTest 复制步骤切片且保留 nil 与空切片的区分，便于检测入参是否被修改。
func clonePlanStepsForTest(steps []Step) []Step {
	if steps == nil {
		return nil
	}
	return append([]Step{}, steps...)
}

// clonePlanCompletedForTest 复制完成集合且保留 nil 与空 map 的区分。
func clonePlanCompletedForTest(completed map[string]bool) map[string]bool {
	if completed == nil {
		return nil
	}
	out := make(map[string]bool, len(completed))
	for k, v := range completed {
		out[k] = v
	}
	return out
}

// cloneStepPlanForTest 深复制 StepPlan，用于检测 AdvancePlan 是否修改入参。
func cloneStepPlanForTest(p StepPlan) StepPlan {
	out := StepPlan{
		Blocked: make(map[string][]string, len(p.Blocked)),
	}
	// 保留 nil 与空切片的区别，避免把空切片误判为被修改。
	if p.Order != nil {
		out.Order = append([]string{}, p.Order...)
	}
	if p.ReadyNow != nil {
		out.ReadyNow = append([]string{}, p.ReadyNow...)
	}
	for name, deps := range p.Blocked {
		if deps != nil {
			out.Blocked[name] = append([]string{}, deps...)
		}
	}
	return out
}

// TestBuildStepPlan 表驱动覆盖线性链、菱形、部分完成、环、无就绪、自引用、CJK
// 名称、重名、args 依赖、畸形引用、显式 false 以及声明顺序与执行顺序不同的场景。
// 同时校验三个字段的精确值与 Order 直接取自 SimulateRun（环/空输入时为 nil）。
func TestBuildStepPlan(t *testing.T) {
	linear := []Step{
		{Name: "a", Prompt: "扫描"},
		{Name: "b", Prompt: "复核 {{steps.a.output}}"},
		{Name: "c", Prompt: "汇总 {{steps.b.output}}"},
	}
	diamond := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.a.output}}"},
		{Name: "c", Prompt: "{{steps.a.output}}"},
		{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
	}

	cases := []struct {
		name      string
		steps     []Step
		completed map[string]bool
		want      StepPlan
	}{
		{
			name: "nil steps yields empty plan",
			want: StepPlan{
				Order:    nil,
				ReadyNow: []string{},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:  "linear chain with nothing completed",
			steps: linear,
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{"a"},
				Blocked:  map[string][]string{"b": {"a"}, "c": {"b"}},
			},
		},
		{
			name:      "linear chain partially completed",
			steps:     linear,
			completed: map[string]bool{"a": true},
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{"b"},
				Blocked:  map[string][]string{"c": {"b"}},
			},
		},
		{
			name:      "linear chain all completed has no ready",
			steps:     linear,
			completed: map[string]bool{"a": true, "b": true, "c": true},
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:  "diamond root ready",
			steps: diamond,
			want: StepPlan{
				Order:    []string{"a", "b", "c", "d"},
				ReadyNow: []string{"a"},
				Blocked: map[string][]string{
					"b": {"a"},
					"c": {"a"},
					"d": {"b", "c"},
				},
			},
		},
		{
			name:      "diamond branches completed fan out to join",
			steps:     diamond,
			completed: map[string]bool{"a": true, "b": true, "c": true},
			want: StepPlan{
				Order:    []string{"a", "b", "c", "d"},
				ReadyNow: []string{"d"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name: "cycle has empty order and no ready",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			},
			want: StepPlan{
				Order:    nil,
				ReadyNow: []string{},
				Blocked:  map[string][]string{"a": {"b"}, "b": {"a"}},
			},
		},
		{
			name:  "self reference is cyclic and blocked on itself",
			steps: []Step{{Name: "a", Prompt: "{{steps.a.output}}"}},
			want: StepPlan{
				Order:    nil,
				ReadyNow: []string{},
				Blocked:  map[string][]string{"a": {"a"}},
			},
		},
		{
			name: "CJK names with partial completion",
			steps: []Step{
				{Name: "扫描", Prompt: "列出改动"},
				{Name: "复核", Prompt: "复核 {{steps.扫描.output}}"},
				{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
			},
			completed: map[string]bool{"扫描": true},
			want: StepPlan{
				Order:    []string{"扫描", "复核", "汇总"},
				ReadyNow: []string{"复核"},
				// StepDependencyMap 将依赖按字典序排序：复核 (U+590D) < 扫描 (U+626B)。
				Blocked: map[string][]string{"汇总": {"复核"}},
			},
		},
		{
			name: "duplicate names keep first declaration and empty order",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "a", Prompt: "{{steps.missing.output}}"},
			},
			want: StepPlan{
				Order:    nil,
				ReadyNow: []string{"a"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name: "ready now follows declaration order but order follows lexicographic",
			steps: []Step{
				{Name: "z", Prompt: "root"},
				{Name: "a", Prompt: "root"},
				{Name: "m", Prompt: "root"},
			},
			want: StepPlan{
				Order:    []string{"a", "m", "z"},
				ReadyNow: []string{"z", "a", "m"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name: "dependency declared only in args",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Tool: "write", Args: `{"x":"{{steps.a.output}}"}`},
			},
			completed: map[string]bool{"a": true},
			want: StepPlan{
				Order:    []string{"a", "b"},
				ReadyNow: []string{"b"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:  "malformed references ignored",
			steps: []Step{{Name: "a", Prompt: "{{steps..output}} {{steps.b}} {{input}}"}},
			want: StepPlan{
				Order:    []string{"a"},
				ReadyNow: []string{"a"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:      "explicit false dependency is still unmet",
			steps:     linear,
			completed: map[string]bool{"a": false},
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{"a"},
				Blocked:  map[string][]string{"b": {"a"}, "c": {"b"}},
			},
		},
		{
			name: "names are trimmed for steps and completed",
			steps: []Step{
				{Name: "  a  ", Prompt: "root"},
				{Name: " b ", Prompt: "{{steps.a.output}}"},
			},
			completed: map[string]bool{"a": true},
			want: StepPlan{
				Order:    []string{"a", "b"},
				ReadyNow: []string{"b"},
				Blocked:  map[string][]string{},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stepsSnapshot := clonePlanStepsForTest(tc.steps)
			completedSnapshot := clonePlanCompletedForTest(tc.completed)

			// Order 必须直接等于 SimulateRun 的结果（含错误时的 nil）。
			wantOrder, _ := SimulateRun(tc.steps)

			got := BuildStepPlan(tc.steps, tc.completed)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("BuildStepPlan() = %#v, want %#v", got, tc.want)
			}
			if !reflect.DeepEqual(got.Order, wantOrder) {
				t.Fatalf("BuildStepPlan().Order = %#v, SimulateRun() = %#v", got.Order, wantOrder)
			}
			if !reflect.DeepEqual(tc.steps, stepsSnapshot) {
				t.Fatalf("BuildStepPlan mutated steps: got %+v, want %+v", tc.steps, stepsSnapshot)
			}
			if !reflect.DeepEqual(tc.completed, completedSnapshot) {
				t.Fatalf("BuildStepPlan mutated completed: got %v, want %v", tc.completed, completedSnapshot)
			}
		})
	}
}

// TestBuildStepPlanStableAndIndependent 确认重复调用结果一致，且修改返回的
// Order/ReadyNow/Blocked 都不会污染入参或后续调用。
func TestBuildStepPlanStableAndIndependent(t *testing.T) {
	steps := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "x", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "y", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "b", Prompt: "root"},
	}
	completed := map[string]bool{"a": true}
	want := BuildStepPlan(steps, completed)

	for i := 0; i < 5; i++ {
		got := BuildStepPlan(steps, completed)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d: BuildStepPlan() = %#v, want %#v", i, got, want)
		}
	}

	// 篡改返回值的每一层，后续调用都应不受影响。
	mutated := BuildStepPlan(steps, completed)
	if len(mutated.Order) > 0 {
		mutated.Order[0] = "mutated-order"
	}
	if len(mutated.ReadyNow) > 0 {
		mutated.ReadyNow[0] = "mutated-ready"
	}
	for name, deps := range mutated.Blocked {
		if len(deps) > 0 {
			deps[0] = "mutated-dep"
		}
		mutated.Blocked[name] = deps
	}
	mutated.Blocked["injected"] = []string{"z"}

	if again := BuildStepPlan(steps, completed); !reflect.DeepEqual(again, want) {
		t.Fatalf("mutating returned plan affected later call: %#v, want %#v", again, want)
	}
	if !reflect.DeepEqual(steps, []Step{
		{Name: "a", Prompt: "root"},
		{Name: "x", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "y", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "b", Prompt: "root"},
	}) {
		t.Fatalf("BuildStepPlan mutated its steps input: %+v", steps)
	}
	if !reflect.DeepEqual(completed, map[string]bool{"a": true}) {
		t.Fatalf("BuildStepPlan mutated its completed input: %v", completed)
	}
}

// TestAdvancePlan 表驱动覆盖线性推进、菱形扇出与汇合、未知/已完成步骤（空操作）、
// 环、CJK 名称、空计划、无依赖计划以及同时解锁多个步骤的确定顺序。
func TestAdvancePlan(t *testing.T) {
	linear := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.a.output}}"},
		{Name: "c", Prompt: "{{steps.b.output}}"},
	}
	diamond := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.a.output}}"},
		{Name: "c", Prompt: "{{steps.a.output}}"},
		{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
	}
	cjk := []Step{
		{Name: "扫描", Prompt: "列出改动"},
		{Name: "复核", Prompt: "复核 {{steps.扫描.output}}"},
		{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
	}

	cases := []struct {
		name          string
		plan          StepPlan
		justCompleted string
		want          StepPlan
	}{
		{
			name:          "linear advance first step",
			plan:          BuildStepPlan(linear, nil),
			justCompleted: "a",
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{"b"},
				Blocked:  map[string][]string{"c": {"b"}},
			},
		},
		{
			name:          "linear advance second step",
			plan:          BuildStepPlan(linear, map[string]bool{"a": true}),
			justCompleted: "b",
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{"c"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:          "diamond advance root unblocks branches",
			plan:          BuildStepPlan(diamond, nil),
			justCompleted: "a",
			want: StepPlan{
				Order:    []string{"a", "b", "c", "d"},
				ReadyNow: []string{"b", "c"},
				Blocked:  map[string][]string{"d": {"b", "c"}},
			},
		},
		{
			name:          "diamond advance one branch keeps join blocked",
			plan:          BuildStepPlan(diamond, map[string]bool{"a": true}),
			justCompleted: "b",
			want: StepPlan{
				Order:    []string{"a", "b", "c", "d"},
				ReadyNow: []string{"c"},
				Blocked:  map[string][]string{"d": {"c"}},
			},
		},
		{
			name:          "diamond advance last branch unblocks join",
			plan:          BuildStepPlan(diamond, map[string]bool{"a": true, "b": true}),
			justCompleted: "c",
			want: StepPlan{
				Order:    []string{"a", "b", "c", "d"},
				ReadyNow: []string{"d"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:          "unknown step is a no-op copy",
			plan:          BuildStepPlan(linear, nil),
			justCompleted: "ghost",
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{"a"},
				Blocked:  map[string][]string{"b": {"a"}, "c": {"b"}},
			},
		},
		{
			name:          "already completed step is a no-op copy",
			plan:          BuildStepPlan(linear, map[string]bool{"a": true}),
			justCompleted: "a",
			want: StepPlan{
				Order:    []string{"a", "b", "c"},
				ReadyNow: []string{"b"},
				Blocked:  map[string][]string{"c": {"b"}},
			},
		},
		{
			name: "cycle keeps nil order but advances readiness",
			plan: BuildStepPlan([]Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			}, nil),
			justCompleted: "a",
			want: StepPlan{
				Order:    nil,
				ReadyNow: []string{"b"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:          "CJK names advance to join",
			plan:          BuildStepPlan(cjk, map[string]bool{"扫描": true}),
			justCompleted: "复核",
			want: StepPlan{
				Order:    []string{"扫描", "复核", "汇总"},
				ReadyNow: []string{"汇总"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name:          "empty plan stays empty",
			plan:          BuildStepPlan(nil, nil),
			justCompleted: "x",
			want: StepPlan{
				Order:    nil,
				ReadyNow: []string{},
				Blocked:  map[string][]string{},
			},
		},
		{
			name: "no dependency plan drops completed step",
			plan: BuildStepPlan([]Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Prompt: "root"},
			}, nil),
			justCompleted: "a",
			want: StepPlan{
				Order:    []string{"a", "b"},
				ReadyNow: []string{"b"},
				Blocked:  map[string][]string{},
			},
		},
		{
			name: "simultaneously unblocked steps use lexicographic order",
			plan: BuildStepPlan([]Step{
				{Name: "z", Prompt: "root"},
				{Name: "y", Prompt: "{{steps.z.output}}"},
				{Name: "x", Prompt: "{{steps.z.output}}"},
			}, nil),
			justCompleted: "z",
			want: StepPlan{
				Order:    []string{"z", "x", "y"},
				ReadyNow: []string{"x", "y"},
				Blocked:  map[string][]string{},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planSnapshot := cloneStepPlanForTest(tc.plan)

			got := AdvancePlan(tc.plan, tc.justCompleted)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("AdvancePlan() = %#v, want %#v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.plan, planSnapshot) {
				t.Fatalf("AdvancePlan mutated its input plan: got %#v, want %#v", tc.plan, planSnapshot)
			}
		})
	}
}

// TestAdvancePlanPureAndIndependent 确认 AdvancePlan 不修改入参，且返回的每个
// 成员都与入参零共享；同时验证 Order 原样保留（不重算）。
func TestAdvancePlanPureAndIndependent(t *testing.T) {
	steps := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
		{Name: "b", Prompt: "{{steps.a.output}}"},
		{Name: "c", Prompt: "{{steps.a.output}}"},
	}
	plan := BuildStepPlan(steps, map[string]bool{"a": true})
	snapshot := cloneStepPlanForTest(plan)
	want := AdvancePlan(plan, "b")
	wantSnapshot := cloneStepPlanForTest(want)

	for i := 0; i < 5; i++ {
		got := AdvancePlan(plan, "b")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d: AdvancePlan() = %#v, want %#v", i, got, want)
		}
		if !reflect.DeepEqual(got.Order, snapshot.Order) {
			t.Fatalf("AdvancePlan recomputed Order: got %#v, want %#v", got.Order, snapshot.Order)
		}
	}

	// 篡改返回值不应影响入参或后续调用。
	got := AdvancePlan(plan, "b")
	if len(got.Order) > 0 {
		got.Order[0] = "mutated-order"
	}
	if len(got.ReadyNow) > 0 {
		got.ReadyNow[0] = "mutated-ready"
	}
	for name, deps := range got.Blocked {
		if len(deps) > 0 {
			deps[0] = "mutated-dep"
		}
		got.Blocked[name] = deps
	}
	got.Blocked["injected"] = []string{"z"}

	if again := AdvancePlan(plan, "b"); !reflect.DeepEqual(again, wantSnapshot) {
		t.Fatalf("mutating returned plan affected later call: %#v, want %#v", again, wantSnapshot)
	}
	if !reflect.DeepEqual(plan, snapshot) {
		t.Fatalf("AdvancePlan mutated its input plan: got %#v, want %#v", plan, snapshot)
	}
}

// TestBuildAdvanceSequence 端到端验证从构建到逐步推进的整条链与直接构造相同
// 完成集的 BuildStepPlan 结果一致（Order 相同，ReadyNow/Blocked 收敛）。
func TestBuildAdvanceSequence(t *testing.T) {
	steps := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.a.output}}"},
		{Name: "c", Prompt: "{{steps.a.output}}"},
		{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
	}
	completed := map[string]bool{}
	plan := BuildStepPlan(steps, completed)
	for _, name := range []string{"a", "b", "c"} {
		plan = AdvancePlan(plan, name)
		completed[name] = true
		want := BuildStepPlan(steps, completed)
		if !reflect.DeepEqual(plan.ReadyNow, want.ReadyNow) {
			t.Fatalf("after %q: ReadyNow = %#v, want %#v", name, plan.ReadyNow, want.ReadyNow)
		}
		if !reflect.DeepEqual(plan.Blocked, want.Blocked) {
			t.Fatalf("after %q: Blocked = %#v, want %#v", name, plan.Blocked, want.Blocked)
		}
		if !reflect.DeepEqual(plan.Order, want.Order) {
			t.Fatalf("after %q: Order = %#v, want %#v", name, plan.Order, want.Order)
		}
	}
	if !reflect.DeepEqual(plan.ReadyNow, []string{"d"}) {
		t.Fatalf("final ReadyNow = %#v, want [d]", plan.ReadyNow)
	}
}
