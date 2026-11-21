package workflow

import (
	"reflect"
	"sort"
	"testing"
)

// TestStepDependencyMap 覆盖依赖图解析的正确性、稳定排序、畸形引用忽略、
// 步骤名归一化与入参不变性。
func TestStepDependencyMap(t *testing.T) {
	cases := []struct {
		name  string
		steps []Step
		want  map[string][]string
	}{
		{
			name: "nil steps yields empty map",
			want: map[string][]string{},
		},
		{
			name: "no dependencies yields non-nil empty slices",
			steps: []Step{
				{Name: "only", Prompt: "do work"},
			},
			want: map[string][]string{"only": {}},
		},
		{
			name: "linear chain",
			steps: []Step{
				{Name: "a", Prompt: "scan"},
				{Name: "b", Prompt: "review {{steps.a.output}}"},
				{Name: "c", Prompt: "report {{steps.b.output}}"},
			},
			want: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"b"},
			},
		},
		{
			name: "diamond fans out and joins",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "c", Prompt: "{{steps.a.output}}"},
				{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
			},
			want: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"a"},
				"d": {"b", "c"},
			},
		},
		{
			name: "sorted across prompt and args",
			steps: []Step{
				{Name: "a", Prompt: "first"},
				{Name: "c", Prompt: "third"},
				// referencedSteps 先收 Prompt 再收 Args，原始顺序是 c, a。
				{Name: "d", Prompt: "{{steps.c.output}}", Tool: "write", Args: `{"x":"{{steps.a.output}}"}`},
			},
			want: map[string][]string{
				"a": {},
				"c": {},
				"d": {"a", "c"},
			},
		},
		{
			name: "deduped across prompt and args",
			steps: []Step{
				{Name: "a", Prompt: "first"},
				{Name: "b", Prompt: "{{steps.a.output}}", Tool: "write", Args: `{"x":"{{steps.a.output}}"}`},
			},
			want: map[string][]string{
				"a": {},
				"b": {"a"},
			},
		},
		{
			name: "malformed and non-step references ignored",
			steps: []Step{
				{Name: "a", Prompt: "{{steps..output}} {{steps.b}} {{input}} {{steps.c.output", Tool: "write", Args: `{"x":"{{param}}"}`},
			},
			want: map[string][]string{"a": {}},
		},
		{
			name: "step names trimmed like other DAG helpers",
			steps: []Step{
				{Name: "  a  ", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "root"},
			},
			want: map[string][]string{
				"a": {"b"},
				"b": {},
			},
		},
		{
			name: "duplicate names keep first declaration",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "root"},
				{Name: "a", Prompt: "{{steps.c.output}}"},
			},
			want: map[string][]string{
				"a": {"b"},
				"b": {},
			},
		},
		{
			name: "CJK step names and dependencies",
			steps: []Step{
				{Name: "扫描", Prompt: "列出改动"},
				{Name: "复核", Prompt: "复核 {{steps.扫描.output}}"},
				{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
			},
			want: map[string][]string{
				"扫描": {},
				"复核": {"扫描"},
				// 复核 (U+590D) 排在 扫描 (U+626B) 之前。
				"汇总": {"复核", "扫描"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := append([]Step(nil), tc.steps...)
			got := StepDependencyMap(tc.steps)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("StepDependencyMap() = %#v, want %#v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.steps, snapshot) {
				t.Fatalf("StepDependencyMap mutated its input: got %+v, want %+v", tc.steps, snapshot)
			}
			for name, deps := range got {
				if !sort.StringsAreSorted(deps) {
					t.Errorf("deps for %q are not sorted: %v", name, deps)
				}
			}
		})
	}
}

// TestStepDependencyMapStableOrder 确认重复调用与打乱步骤入参顺序都不改变输出。
func TestStepDependencyMapStableOrder(t *testing.T) {
	base := []Step{
		{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
		{Name: "扫描", Prompt: "列出改动"},
		{Name: "复核", Prompt: "{{steps.扫描.output}}", Tool: "write", Args: `{"x":"{{steps.扫描.output}}"}`},
	}
	want := StepDependencyMap(base)

	for i := 0; i < 5; i++ {
		got := StepDependencyMap(base)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d: StepDependencyMap() = %#v, want %#v", i, got, want)
		}
	}

	// 每种步骤排列都应得到相同的映射。
	perms := [][]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
	for _, perm := range perms {
		shuffled := make([]Step, 0, len(base))
		for _, idx := range perm {
			shuffled = append(shuffled, base[idx])
		}
		got := StepDependencyMap(shuffled)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("perm %v: StepDependencyMap() = %#v, want %#v", perm, got, want)
		}
	}
}

// TestStepDependencyMapReturnsIndependentCopies 确认返回值与入参、内部状态以及
// 其它条目之间都不共享底层数组。
func TestStepDependencyMapReturnsIndependentCopies(t *testing.T) {
	steps := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "x", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "y", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "b", Prompt: "root"},
	}
	want := map[string][]string{
		"a": {},
		"x": {"a", "b"},
		"y": {"a", "b"},
		"b": {},
	}

	got := StepDependencyMap(steps)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StepDependencyMap() = %#v, want %#v", got, want)
	}

	// 修改一个条目的切片不应波及另一个同依赖条目。
	got["x"][0] = "mutated"
	if !reflect.DeepEqual(got["y"], []string{"a", "b"}) {
		t.Fatalf("mutating x deps also changed y deps: %v", got["y"])
	}

	// 修改 map 结构（新增/删除）不应影响下一次调用。
	delete(got, "a")
	got["injected"] = []string{"z"}
	again := StepDependencyMap(steps)
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("mutating returned map affected later call: %#v, want %#v", again, want)
	}
	if !reflect.DeepEqual(steps, []Step{
		{Name: "a", Prompt: "root"},
		{Name: "x", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "y", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
		{Name: "b", Prompt: "root"},
	}) {
		t.Fatalf("StepDependencyMap mutated its input: %+v", steps)
	}
}

// TestIsStepReady 覆盖就绪判定的各种完成集合，并确认函数是纯函数。
func TestIsStepReady(t *testing.T) {
	cases := []struct {
		name      string
		step      Step
		completed map[string]bool
		want      bool
	}{
		{
			name: "no dependencies with nil completed",
			step: Step{Name: "a", Prompt: "root"},
			want: true,
		},
		{
			name:      "no dependencies with empty completed",
			step:      Step{Name: "a", Prompt: "root"},
			completed: map[string]bool{},
			want:      true,
		},
		{
			name:      "single dependency completed",
			step:      Step{Name: "b", Prompt: "{{steps.a.output}}"},
			completed: map[string]bool{"a": true},
			want:      true,
		},
		{
			name: "single dependency missing with nil completed",
			step: Step{Name: "b", Prompt: "{{steps.a.output}}"},
			want: false,
		},
		{
			name:      "single dependency explicitly false",
			step:      Step{Name: "b", Prompt: "{{steps.a.output}}"},
			completed: map[string]bool{"a": false},
			want:      false,
		},
		{
			name:      "partial dependencies complete",
			step:      Step{Name: "c", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
			completed: map[string]bool{"a": true},
			want:      false,
		},
		{
			name:      "all dependencies complete with extra keys",
			step:      Step{Name: "c", Prompt: "{{steps.a.output}} {{steps.b.output}}"},
			completed: map[string]bool{"a": true, "b": true, "unrelated": true},
			want:      true,
		},
		{
			name:      "dependency declared only in args",
			step:      Step{Name: "d", Tool: "write", Args: `{"x":"{{steps.a.output}}"}`},
			completed: map[string]bool{"a": true},
			want:      true,
		},
		{
			name:      "CJK dependency completed",
			step:      Step{Name: "汇总", Prompt: "{{steps.扫描.output}}"},
			completed: map[string]bool{"扫描": true},
			want:      true,
		},
		{
			name:      "CJK dependency missing",
			step:      Step{Name: "汇总", Prompt: "{{steps.扫描.output}}"},
			completed: map[string]bool{"复核": true},
			want:      false,
		},
		{
			name:      "malformed references ignored",
			step:      Step{Name: "a", Prompt: "{{steps..output}} {{steps.b}} {{input}}"},
			completed: nil,
			want:      true,
		},
		{
			name:      "self reference is not ready until itself completed",
			step:      Step{Name: "a", Prompt: "{{steps.a.output}}"},
			completed: map[string]bool{},
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stepSnapshot := tc.step
			var completedSnapshot map[string]bool
			if tc.completed != nil {
				completedSnapshot = make(map[string]bool, len(tc.completed))
				for k, v := range tc.completed {
					completedSnapshot[k] = v
				}
			}

			got := IsStepReady(tc.step, tc.completed)
			if got != tc.want {
				t.Fatalf("IsStepReady() = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.step, stepSnapshot) {
				t.Fatalf("IsStepReady mutated its step: got %+v, want %+v", tc.step, stepSnapshot)
			}
			if !reflect.DeepEqual(tc.completed, completedSnapshot) {
				t.Fatalf("IsStepReady mutated completed: got %v, want %v", tc.completed, completedSnapshot)
			}
		})
	}
}
