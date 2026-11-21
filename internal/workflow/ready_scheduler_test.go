package workflow

import (
	"errors"
	"reflect"
	"testing"
)

// cloneSteps 复制步骤切片且保留 nil 与空切片的区分，便于精确检测入参是否被修改。
func cloneSteps(steps []Step) []Step {
	if steps == nil {
		return nil
	}
	return append([]Step{}, steps...)
}

// TestReadySteps 表驱动覆盖线性链、菱形、部分完成、无就绪、环、CJK 名以及
// 依赖解析位置等场景，并校验返回顺序始终跟随步骤声明顺序。
func TestReadySteps(t *testing.T) {
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
		want      []string
	}{
		{
			name: "nil steps yields empty slice",
			want: []string{},
		},
		{
			name:  "no dependencies with nil completed",
			steps: []Step{{Name: "a"}, {Name: "b"}},
			want:  []string{"a", "b"},
		},
		{
			name:  "linear chain with nothing completed",
			steps: linear,
			want:  []string{"a"},
		},
		{
			name:      "linear chain partially completed",
			steps:     linear,
			completed: map[string]bool{"a": true},
			want:      []string{"b"},
		},
		{
			name:      "linear chain all but last completed",
			steps:     linear,
			completed: map[string]bool{"a": true, "b": true},
			want:      []string{"c"},
		},
		{
			name:      "linear chain all completed",
			steps:     linear,
			completed: map[string]bool{"a": true, "b": true, "c": true},
			want:      []string{},
		},
		{
			name:      "extra completed keys are ignored",
			steps:     linear,
			completed: map[string]bool{"a": true, "unrelated": true},
			want:      []string{"b"},
		},
		{
			name:      "explicit false dependency is not completed",
			steps:     linear,
			completed: map[string]bool{"a": false},
			want:      []string{"a"},
		},
		{
			name:  "diamond root ready",
			steps: diamond,
			want:  []string{"a"},
		},
		{
			name:      "diamond root completed fans out",
			steps:     diamond,
			completed: map[string]bool{"a": true},
			want:      []string{"b", "c"},
		},
		{
			name:      "diamond one branch completed",
			steps:     diamond,
			completed: map[string]bool{"a": true, "b": true},
			want:      []string{"c"},
		},
		{
			name:      "diamond branches completed",
			steps:     diamond,
			completed: map[string]bool{"a": true, "b": true, "c": true},
			want:      []string{"d"},
		},
		{
			name: "cycle has no ready step",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			},
			want: []string{},
		},
		{
			name:  "self reference is never ready",
			steps: []Step{{Name: "a", Prompt: "{{steps.a.output}}"}},
			want:  []string{},
		},
		{
			name: "dependency declared only in args",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Tool: "write", Args: `{"x":"{{steps.a.output}}"}`},
			},
			completed: map[string]bool{"a": true},
			want:      []string{"b"},
		},
		{
			name: "CJK step names and dependencies",
			steps: []Step{
				{Name: "扫描", Prompt: "列出改动"},
				{Name: "复核", Prompt: "复核 {{steps.扫描.output}}"},
				{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
			},
			completed: map[string]bool{"扫描": true, "复核": true},
			want:      []string{"汇总"},
		},
		{
			name: "names are trimmed for both steps and completed",
			steps: []Step{
				{Name: "  a  ", Prompt: "root"},
				{Name: " b ", Prompt: "{{steps.a.output}}"},
			},
			completed: map[string]bool{"a": true},
			want:      []string{"b"},
		},
		{
			name:  "malformed references ignored",
			steps: []Step{{Name: "a", Prompt: "{{steps..output}} {{steps.b}} {{input}}"}},
			want:  []string{"a"},
		},
		{
			name: "duplicate names keep first declaration",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "a", Prompt: "{{steps.missing.output}}"},
			},
			want: []string{"a"},
		},
		{
			name: "declaration order wins over lexicographic order",
			steps: []Step{
				{Name: "z", Prompt: "root"},
				{Name: "a", Prompt: "root"},
				{Name: "m", Prompt: "root"},
			},
			want: []string{"z", "a", "m"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stepsSnapshot := cloneSteps(tc.steps)
			var completedSnapshot map[string]bool
			if tc.completed != nil {
				completedSnapshot = make(map[string]bool, len(tc.completed))
				for k, v := range tc.completed {
					completedSnapshot[k] = v
				}
			}

			got := ReadySteps(tc.steps, tc.completed)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ReadySteps() = %#v, want %#v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.steps, stepsSnapshot) {
				t.Fatalf("ReadySteps mutated steps: got %+v, want %+v", tc.steps, stepsSnapshot)
			}
			if !reflect.DeepEqual(tc.completed, completedSnapshot) {
				t.Fatalf("ReadySteps mutated completed: got %v, want %v", tc.completed, completedSnapshot)
			}
		})
	}
}

// TestReadyStepsStableAndIndependent 确认重复调用顺序一致，且修改返回的切片
// 不会污染后续调用或入参。
func TestReadyStepsStableAndIndependent(t *testing.T) {
	steps := []Step{
		{Name: "z", Prompt: "root"},
		{Name: "a", Prompt: "{{steps.z.output}}"},
		{Name: "m", Prompt: "{{steps.z.output}}"},
	}
	completed := map[string]bool{"z": true}
	want := []string{"a", "m"}

	for i := 0; i < 5; i++ {
		got := ReadySteps(steps, completed)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d: ReadySteps() = %#v, want %#v", i, got, want)
		}
	}

	got := ReadySteps(steps, completed)
	got[0] = "mutated"
	got = append(got, "extra")
	if again := ReadySteps(steps, completed); !reflect.DeepEqual(again, want) {
		t.Fatalf("mutating returned slice affected later call: %#v, want %#v", again, want)
	}
	if !reflect.DeepEqual(steps, []Step{
		{Name: "z", Prompt: "root"},
		{Name: "a", Prompt: "{{steps.z.output}}"},
		{Name: "m", Prompt: "{{steps.z.output}}"},
	}) {
		t.Fatalf("ReadySteps mutated its input: %+v", steps)
	}
	if !reflect.DeepEqual(completed, map[string]bool{"z": true}) {
		t.Fatalf("ReadySteps mutated completed: %v", completed)
	}
}

// TestSimulateRun 表驱动覆盖纯模拟的执行顺序、输入排列无关性、CJK 名与各类
// 图错误，并确认模拟结果与 StepExecutionOrder 逐字节一致。
func TestSimulateRun(t *testing.T) {
	cases := []struct {
		name    string
		steps   []Step
		want    []string
		wantErr error // 期望可用 errors.Is 匹配的哨兵；nil 表示应成功
	}{
		{
			name: "nil steps",
			want: nil,
		},
		{
			name:  "empty steps",
			steps: []Step{},
			want:  nil,
		},
		{
			name: "linear chain",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "c", Prompt: "{{steps.b.output}}"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "forward reference declared later",
			steps: []Step{
				{Name: "consume", Prompt: "consume {{steps.produce.output}}"},
				{Name: "produce", Prompt: "produce data"},
			},
			want: []string{"produce", "consume"},
		},
		{
			name: "diamond with lexicographic tie break",
			steps: []Step{
				{Name: "d", Prompt: "{{steps.c.output}} {{steps.b.output}}"},
				{Name: "c", Prompt: "{{steps.a.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "a", Prompt: "root"},
			},
			want: []string{"a", "b", "c", "d"},
		},
		{
			name: "CJK step names",
			steps: []Step{
				{Name: "汇总", Prompt: "{{steps.扫描.output}} {{steps.复核.output}}"},
				{Name: "复核", Prompt: "{{steps.扫描.output}}"},
				{Name: "扫描", Prompt: "列出改动"},
			},
			want: []string{"扫描", "复核", "汇总"},
		},
		{
			name: "cycle",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			},
			wantErr: ErrStepDependencyCycle,
		},
		{
			name: "self reference is a cycle",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.a.output}}"},
			},
			wantErr: ErrStepDependencyCycle,
		},
		{
			name: "missing dependency",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.ghost.output}}"},
			},
			wantErr: ErrStepMissingDependency,
		},
		{
			name: "duplicate name",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "a", Prompt: "again"},
			},
			wantErr: ErrDuplicateStepName,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := cloneSteps(tc.steps)
			wantOrder, wantErr := StepExecutionOrder(tc.steps)

			order, err := SimulateRun(tc.steps)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("SimulateRun() error = nil, want %v", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SimulateRun() error = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				if order != nil {
					t.Fatalf("SimulateRun() order = %#v on error, want nil", order)
				}
			} else {
				if err != nil {
					t.Fatalf("SimulateRun() error = %v, want nil", err)
				}
				if !reflect.DeepEqual(order, tc.want) {
					t.Fatalf("SimulateRun() = %#v, want %#v", order, tc.want)
				}
			}
			// 纯函数语义：结果与 StepExecutionOrder 完全一致。诊断错误每次调用
			// 都会重新构造，不能直接比较身份，只比较成功/失败是否一致。
			if !reflect.DeepEqual(order, wantOrder) || (err == nil) != (wantErr == nil) {
				t.Fatalf("SimulateRun() = (%#v, %v), StepExecutionOrder() = (%#v, %v)", order, err, wantOrder, wantErr)
			}
			if !reflect.DeepEqual(tc.steps, snapshot) {
				t.Fatalf("SimulateRun mutated its input: got %+v, want %+v", tc.steps, snapshot)
			}
		})
	}
}

// TestSimulateRunIgnoresDeclarationPermutation 确认模拟只依赖图结构，打乱声明顺序不改变结果。
func TestSimulateRunIgnoresDeclarationPermutation(t *testing.T) {
	base := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.a.output}}"},
		{Name: "c", Prompt: "{{steps.a.output}}"},
		{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
	}
	want := []string{"a", "b", "c", "d"}
	perms := [][]int{
		{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 3, 0, 2}, {2, 0, 3, 1},
	}
	for _, perm := range perms {
		shuffled := make([]Step, 0, len(base))
		for _, idx := range perm {
			shuffled = append(shuffled, base[idx])
		}
		order, err := SimulateRun(shuffled)
		if err != nil {
			t.Fatalf("perm %v: SimulateRun() error = %v", perm, err)
		}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("perm %v: SimulateRun() = %#v, want %#v", perm, order, want)
		}
	}
}
