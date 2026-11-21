package skills

import (
	"reflect"
	"testing"
)

func TestCheckPrereqs(t *testing.T) {
	goTool := Prereq{Name: "go", Kind: "tool", Value: ">=1.27"}
	homeEnv := Prereq{Name: "HOME", Kind: "env", Value: "any"}
	themeCfg := Prereq{Name: "theme", Kind: "config", Value: "dark"}
	gitTool := Prereq{Name: "git", Kind: "tool", Value: "any"}
	cjkTool := Prereq{Name: "部署工具", Kind: "tool", Value: "任意版本"}
	cjkEnv := Prereq{Name: "区域设置", Kind: "env", Value: "zh_CN"}

	cases := []struct {
		name      string
		prereqs   []Prereq
		eval      PrereqEvaluator
		wantUnmet []Prereq
	}{
		{
			name:      "all satisfied",
			prereqs:   []Prereq{goTool, homeEnv, themeCfg},
			eval:      func(Prereq) bool { return true },
			wantUnmet: nil,
		},
		{
			name:      "partial unmet keeps original order",
			prereqs:   []Prereq{goTool, homeEnv, themeCfg},
			eval:      func(p Prereq) bool { return p.Kind == "env" },
			wantUnmet: []Prereq{goTool, themeCfg},
		},
		{
			name:    "stable order with interleaved unmet",
			prereqs: []Prereq{themeCfg, goTool, homeEnv, gitTool},
			// 第 0、1、3 条未满足，第 2 条满足；结果必须保持 0、1、3 的原始顺序。
			eval:      func(p Prereq) bool { return p.Kind == "env" },
			wantUnmet: []Prereq{themeCfg, goTool, gitTool},
		},
		{
			name:    "nil evaluator treats all as satisfied",
			prereqs: []Prereq{goTool, homeEnv, themeCfg},
			eval:    nil,
			// 即使这些条件实际都不满足，nil eval 也视为全部满足。
			wantUnmet: nil,
		},
		{
			name:      "empty input",
			prereqs:   nil,
			eval:      func(Prereq) bool { return false },
			wantUnmet: nil,
		},
		{
			name:      "cjk names preserved verbatim",
			prereqs:   []Prereq{cjkTool, cjkEnv},
			eval:      func(Prereq) bool { return false },
			wantUnmet: []Prereq{cjkTool, cjkEnv},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckPrereqs(tc.prereqs, tc.eval)
			if err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tc.wantUnmet) {
				t.Errorf("unmet = %v, want %v", got, tc.wantUnmet)
			}
		})
	}
}

// CheckPrereqs 必须是纯函数：不修改传入的切片。
func TestCheckPrereqsDoesNotModifyInput(t *testing.T) {
	prereqs := []Prereq{
		{Name: "go", Kind: "tool", Value: ">=1.27"},
		{Name: "HOME", Kind: "env", Value: "any"},
	}
	orig := []Prereq{
		{Name: "go", Kind: "tool", Value: ">=1.27"},
		{Name: "HOME", Kind: "env", Value: "any"},
	}

	// 全部判为不满足，走一遍完整分支，确保没有任何写回。
	unmet, err := CheckPrereqs(prereqs, func(Prereq) bool { return false })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !reflect.DeepEqual(prereqs, orig) {
		t.Fatalf("prereqs was modified: %v, want %v", prereqs, orig)
	}

	// 改动返回的 unmet 也不应影响入参（结果不是入参的别名）。
	unmet[0].Name = "mutated"
	if !reflect.DeepEqual(prereqs, orig) {
		t.Fatalf("mutating unmet changed prereqs: %v, want %v", prereqs, orig)
	}
}

// nil eval 不产生副作用：不调用任何回调，也不写入任何结果。
func TestCheckPrereqsNilEvaluatorNoCalls(t *testing.T) {
	prereqs := []Prereq{{Name: "go", Kind: "tool", Value: ">=1.27"}}

	// 用一个会把调用记下来的闭包，验证一旦传入就必须每条都调用一次；
	// 随后传 nil，验证没有任何调用发生、且结果与"全部满足"一致。
	calls := 0
	if _, err := CheckPrereqs(prereqs, func(Prereq) bool { calls++; return true }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != len(prereqs) {
		t.Fatalf("non-nil eval called %d times, want %d", calls, len(prereqs))
	}

	calls = 0
	// nil 无法再计入 calls，这里验证它不 panic、不返回未满足项。
	got, err := CheckPrereqs(prereqs, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("nil eval: unmet = %v, err = %v; want empty unmet and nil err", got, err)
	}
	if calls != 0 {
		t.Fatalf("nil eval unexpectedly left %d recorded calls", calls)
	}
}

func TestPrereqSummary(t *testing.T) {
	cases := []struct {
		name    string
		prereqs []Prereq
		want    map[string]int
	}{
		{
			name:    "nil input yields empty non-nil map",
			prereqs: nil,
			want:    map[string]int{},
		},
		{
			name:    "empty input",
			prereqs: []Prereq{},
			want:    map[string]int{},
		},
		{
			name: "counts by kind",
			prereqs: []Prereq{
				{Name: "go", Kind: "tool"},
				{Name: "git", Kind: "tool"},
				{Name: "HOME", Kind: "env"},
				{Name: "theme", Kind: "config"},
				{Name: "no-kind"}, // 空 Kind 也单独计数
			},
			want: map[string]int{"tool": 2, "env": 1, "config": 1, "": 1},
		},
		{
			name: "cjk kind and names",
			prereqs: []Prereq{
				{Name: "部署工具", Kind: "工具"},
				{Name: "git", Kind: "工具"},
				{Name: "区域设置", Kind: "环境"},
			},
			want: map[string]int{"工具": 2, "环境": 1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrereqSummary(tc.prereqs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("summary = %v, want %v", got, tc.want)
			}
		})
	}
}

// PrereqSummary 返回独立 map：改动结果不影响入参计数逻辑，重复调用结果一致。
func TestPrereqSummaryDoesNotModifyInput(t *testing.T) {
	prereqs := []Prereq{
		{Name: "go", Kind: "tool"},
		{Name: "HOME", Kind: "env"},
	}
	orig := []Prereq{
		{Name: "go", Kind: "tool"},
		{Name: "HOME", Kind: "env"},
	}

	summary := PrereqSummary(prereqs)
	if !reflect.DeepEqual(prereqs, orig) {
		t.Fatalf("prereqs was modified: %v, want %v", prereqs, orig)
	}

	summary["tool"] = 99
	if again := PrereqSummary(prereqs); again["tool"] != 1 {
		t.Fatalf("mutating returned map affected recount: %v", again)
	}
	if !reflect.DeepEqual(prereqs, orig) {
		t.Fatalf("prereqs was modified after map mutation: %v, want %v", prereqs, orig)
	}
}
