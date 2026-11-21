package workflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// TestValidateStepDAG 覆盖步骤依赖图的各类合法与非法定义，
// 校验哨兵错误、路径诊断，并确认函数是纯函数（不修改入参）。
func TestValidateStepDAG(t *testing.T) {
	cases := []struct {
		name        string
		steps       []Step
		wantErr     error  // 期望可被 errors.Is 匹配的哨兵；nil 表示应成功
		wantSubstr  string // 错误文本中应出现的子串（路径诊断）
		avoidSubstr string // 错误文本中不应出现的子串
	}{
		{
			name:  "empty definition",
			steps: nil,
		},
		{
			name: "linear chain",
			steps: []Step{
				{Name: "a", Prompt: "scan"},
				{Name: "b", Prompt: "review {{steps.a.output}}"},
				{Name: "c", Prompt: "report {{steps.b.output}}"},
			},
		},
		{
			name: "diamond",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "c", Prompt: "{{steps.a.output}}"},
				{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
			},
		},
		{
			name: "forward reference",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.c.output}}"},
				{Name: "b", Prompt: "independent"},
				{Name: "c", Prompt: "produces"},
			},
		},
		{
			name: "two step cycle",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			},
			wantErr:    ErrStepDependencyCycle,
			wantSubstr: "a -> b -> a",
		},
		{
			name: "self reference is a cycle",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.a.output}}"},
			},
			wantErr:    ErrStepDependencyCycle,
			wantSubstr: "a -> a",
		},
		{
			name: "cycle with dependent step reports path",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "c", Prompt: "{{steps.a.output}}"},
			},
			wantErr:    ErrStepDependencyCycle,
			wantSubstr: " -> ",
		},
		{
			name: "missing dependency",
			steps: []Step{
				{Name: "a", Prompt: "review {{steps.ghost.output}}"},
			},
			wantErr:    ErrStepMissingDependency,
			wantSubstr: `"a" -> "ghost"`,
		},
		{
			name: "missing dependency in args",
			steps: []Step{
				{Name: "a", Tool: "write", Args: `{"body":"{{steps.later.output}}"}`},
			},
			wantErr:    ErrStepMissingDependency,
			wantSubstr: "later",
		},
		{
			name: "duplicate names",
			steps: []Step{
				{Name: "a", Prompt: "first"},
				{Name: "b", Prompt: "second"},
				{Name: "a", Prompt: "third"},
			},
			wantErr:     ErrDuplicateStepName,
			wantSubstr:  `"a"`,
			avoidSubstr: "pluginapi",
		},
		{
			name: "duplicate names after trimming whitespace",
			steps: []Step{
				{Name: " a ", Prompt: "first"},
				{Name: "a", Prompt: "second"},
			},
			wantErr:    ErrDuplicateStepName,
			wantSubstr: `"a"`,
		},
		{
			name: "CJK normal graph",
			steps: []Step{
				{Name: "扫描", Prompt: "列出改动"},
				{Name: "复核", Prompt: "复核 {{steps.扫描.output}}"},
				{Name: "报告", Prompt: "汇总 {{steps.复核.output}}"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := append([]Step(nil), tc.steps...)
			err := ValidateStepDAG(tc.steps)

			if !reflect.DeepEqual(tc.steps, snapshot) {
				t.Fatalf("ValidateStepDAG mutated its input: got %+v, want %+v", tc.steps, snapshot)
			}

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateStepDAG() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateStepDAG() error = nil, want errors.Is(%v)", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.wantErr)
			}
			if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantSubstr)
			}
			if tc.avoidSubstr != "" && strings.Contains(err.Error(), tc.avoidSubstr) {
				t.Fatalf("error = %q, want it NOT to contain %q", err, tc.avoidSubstr)
			}
		})
	}
}

// TestValidateStepDAGWrapsPluginapi 确认底层哨兵也可被 errors.Is 匹配，
// 便于调用方按需要区分「本包校验失败」与「通用拓扑错误」。
func TestValidateStepDAGWrapsPluginapi(t *testing.T) {
	pluginapiCases := []struct {
		name    string
		steps   []Step
		wantErr error
	}{
		{
			name: "cycle wraps pluginapi.ErrCycle",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			},
			wantErr: pluginapi.ErrCycle,
		},
		{
			name: "missing wraps pluginapi.ErrMissingDependency",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.ghost.output}}"},
			},
			wantErr: pluginapi.ErrMissingDependency,
		},
	}
	for _, tc := range pluginapiCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStepDAG(tc.steps)
			if err == nil || !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

// TestStepExecutionOrder 验证合法执行顺序与稳定 tie-break。
func TestStepExecutionOrder(t *testing.T) {
	cases := []struct {
		name    string
		steps   []Step
		want    []string
		wantErr error
	}{
		{
			name:  "empty definition",
			steps: nil,
			want:  nil,
		},
		{
			name: "linear chain",
			steps: []Step{
				{Name: "a", Prompt: "scan"},
				{Name: "b", Prompt: "review {{steps.a.output}}"},
				{Name: "c", Prompt: "report {{steps.b.output}}"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "diamond",
			steps: []Step{
				{Name: "a", Prompt: "root"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
				{Name: "c", Prompt: "{{steps.a.output}}"},
				{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
			},
			want: []string{"a", "b", "c", "d"},
		},
		{
			name: "forward reference is topologically sorted",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.c.output}}"},
				{Name: "b", Prompt: "independent"},
				{Name: "c", Prompt: "produces"},
			},
			want: []string{"b", "c", "a"},
		},
		{
			name: "independent steps tie-break by name",
			steps: []Step{
				{Name: "c", Prompt: "c"},
				{Name: "a", Prompt: "a"},
				{Name: "b", Prompt: "b"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "cycle returns nil order",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.b.output}}"},
				{Name: "b", Prompt: "{{steps.a.output}}"},
			},
			wantErr: ErrStepDependencyCycle,
		},
		{
			name: "missing dependency returns nil order",
			steps: []Step{
				{Name: "a", Prompt: "{{steps.ghost.output}}"},
			},
			wantErr: ErrStepMissingDependency,
		},
		{
			name: "duplicate name returns nil order",
			steps: []Step{
				{Name: "a", Prompt: "first"},
				{Name: "a", Prompt: "second"},
			},
			wantErr: ErrDuplicateStepName,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := append([]Step(nil), tc.steps...)
			order, err := StepExecutionOrder(tc.steps)

			if !reflect.DeepEqual(tc.steps, snapshot) {
				t.Fatalf("StepExecutionOrder mutated its input: got %+v, want %+v", tc.steps, snapshot)
			}

			if tc.wantErr != nil {
				if order != nil {
					t.Fatalf("order = %v, want nil on error", order)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("StepExecutionOrder() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(order, tc.want) {
				t.Fatalf("order = %v, want %v", order, tc.want)
			}
		})
	}
}

// TestStepExecutionOrderShuffledInputSameOutput 确认执行顺序与入参排列无关。
func TestStepExecutionOrderShuffledInputSameOutput(t *testing.T) {
	base := []Step{
		{Name: "a", Prompt: "root"},
		{Name: "b", Prompt: "{{steps.a.output}} {{steps.e.output}}"},
		{Name: "c", Prompt: "{{steps.a.output}}"},
		{Name: "d", Prompt: "{{steps.b.output}} {{steps.c.output}}"},
		{Name: "e", Prompt: "independent"},
		{Name: "f", Prompt: "{{steps.d.output}}"},
	}
	want, err := StepExecutionOrder(base)
	if err != nil {
		t.Fatalf("StepExecutionOrder(base): %v", err)
	}

	// 若干确定的排列：逆序、轮转、以及交错。
	permutations := [][]int{
		{5, 4, 3, 2, 1, 0},
		{2, 0, 5, 1, 4, 3},
		{4, 3, 5, 0, 2, 1},
	}
	for _, perm := range permutations {
		steps := make([]Step, len(perm))
		for i, idx := range perm {
			steps[i] = base[idx]
		}
		got, err := StepExecutionOrder(steps)
		if err != nil {
			t.Fatalf("StepExecutionOrder(shuffled %v): %v", perm, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffled %v order = %v, want %v", perm, got, want)
		}
	}
}

// TestValidateAndOrderAgree 确认两个函数对同一组步骤的合法性判断一致。
func TestValidateAndOrderAgree(t *testing.T) {
	cases := []struct {
		name  string
		steps []Step
	}{
		{name: "valid", steps: []Step{
			{Name: "a", Prompt: "scan"},
			{Name: "b", Prompt: "{{steps.a.output}}"},
		}},
		{name: "cycle", steps: []Step{
			{Name: "a", Prompt: "{{steps.b.output}}"},
			{Name: "b", Prompt: "{{steps.a.output}}"},
		}},
		{name: "missing", steps: []Step{
			{Name: "a", Prompt: "{{steps.ghost.output}}"},
		}},
		{name: "duplicate", steps: []Step{
			{Name: "a"},
			{Name: "a"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validateErr := ValidateStepDAG(tc.steps)
			order, orderErr := StepExecutionOrder(tc.steps)
			if (validateErr == nil) != (orderErr == nil) {
				t.Fatalf("ValidateStepDAG error = %v, StepExecutionOrder error = %v; disagree", validateErr, orderErr)
			}
			if validateErr == nil && len(order) != len(tc.steps) {
				t.Fatalf("order = %v, want %d step names", order, len(tc.steps))
			}
		})
	}
}
