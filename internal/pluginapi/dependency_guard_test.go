package pluginapi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestValidateFeatureDeps(t *testing.T) {
	tests := []struct {
		name     string
		features []Feature
		wantErr  error
		wantText string // 期望出现在错误文本中的片段，空表示不检查
	}{
		{
			name: "valid DAG",
			features: []Feature{
				{Name: "c", Deps: []string{"a", "b"}},
				{Name: "a"},
				{Name: "b", Deps: []string{"a"}},
			},
		},
		{
			name:     "empty input",
			features: nil,
		},
		{
			name:     "duplicate name",
			features: []Feature{{Name: "a"}, {Name: "a", Order: 1}},
			wantErr:  ErrDuplicateFeature,
			wantText: `"a"`,
		},
		{
			name:     "missing dependency",
			features: []Feature{{Name: "a", Deps: []string{"ghost"}}},
			wantErr:  ErrMissingDependency,
			wantText: `"a" -> "ghost"`,
		},
		{
			name: "cycle",
			features: []Feature{
				{Name: "a", Deps: []string{"b"}},
				{Name: "b", Deps: []string{"a"}},
			},
			wantErr:  ErrCycle,
			wantText: "a -> b -> a",
		},
		{
			name:     "self loop",
			features: []Feature{{Name: "a", Deps: []string{"a"}}},
			wantErr:  ErrCycle,
			wantText: "a -> a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFeatureDeps(tt.features)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateFeatureDeps() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateFeatureDeps() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q does not contain %q", err, tt.wantText)
			}
		})
	}
}

func TestExpectedOrder(t *testing.T) {
	// 无依赖且 Order 递增，TopoSort 的期望输出为 [a b c d]。
	features := []Feature{
		{Name: "a", Order: 0},
		{Name: "b", Order: 1},
		{Name: "c", Order: 2},
		{Name: "d", Order: 3},
	}

	tests := []struct {
		name     string
		features []Feature
		current  []string
		wantErr  error
		wantText string // 期望出现在错误文本中的片段，空表示不检查
	}{
		{
			name:     "exact match",
			features: features,
			current:  []string{"a", "b", "c", "d"},
		},
		{
			name:     "nil and empty both mean empty order",
			features: nil,
			current:  []string{},
		},
		{
			name:     "first mismatch index reported",
			features: features,
			current:  []string{"a", "x", "y", "d"},
			wantErr:  ErrOrderDrift,
			wantText: "index 1",
		},
		{
			name:     "mismatch carries got and want names",
			features: features,
			current:  []string{"a", "x", "y", "d"},
			wantErr:  ErrOrderDrift,
			wantText: `got "x", want "b"`,
		},
		{
			name:     "length mismatch shorter",
			features: features,
			current:  []string{"a", "b"},
			wantErr:  ErrOrderDrift,
			wantText: "got 2, want 4",
		},
		{
			name:     "length mismatch longer",
			features: features,
			current:  []string{"a", "b", "c", "d", "e"},
			wantErr:  ErrOrderDrift,
			wantText: "got 5, want 4",
		},
		{
			name:     "declaration error propagated",
			features: []Feature{{Name: "a", Deps: []string{"ghost"}}},
			current:  []string{"a"},
			wantErr:  ErrMissingDependency,
		},
		{
			name:     "cycle error propagated",
			features: []Feature{{Name: "a", Deps: []string{"a"}}},
			current:  []string{"a"},
			wantErr:  ErrCycle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ExpectedOrder(tt.features, tt.current)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ExpectedOrder() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ExpectedOrder() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error %q does not contain %q", err, tt.wantText)
			}
		})
	}
}

func TestStableKey(t *testing.T) {
	tests := []struct {
		name string
		f    Feature
		want string
	}{
		{name: "basic", f: Feature{Name: "tool", Order: 3}, want: "tool@3"},
		{name: "zero order", f: Feature{Name: "tool"}, want: "tool@0"},
		{name: "negative order", f: Feature{Name: "tool", Order: -2}, want: "tool@-2"},
		{name: "CJK name", f: Feature{Name: "工具", Order: 7}, want: "工具@7"},
		{name: "empty name", f: Feature{Order: 1}, want: "@1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StableKey(tt.f)
			if got != tt.want {
				t.Fatalf("StableKey() = %q, want %q", got, tt.want)
			}
			// 稳定性：同一入参重复调用结果必须一致。
			if again := StableKey(tt.f); again != got {
				t.Fatalf("StableKey() not stable: %q then %q", got, again)
			}
		})
	}
}

func TestStableKeyIncludesOrder(t *testing.T) {
	a := StableKey(Feature{Name: "tool", Order: 1})
	b := StableKey(Feature{Name: "tool", Order: 2})
	if a == b {
		t.Fatalf("StableKey must include Order: same key %q for different orders", a)
	}
}

func TestGuardFunctionsDoNotMutateInput(t *testing.T) {
	features := []Feature{
		{Name: "b", Deps: []string{"a", "a"}, Order: 2},
		{Name: "a", Order: 1},
	}
	current := []string{"a", "b"}

	// 深拷贝快照：Deps 单独复制，便于精确比较是否发生写回。
	featureSnapshot := make([]Feature, len(features))
	for i, f := range features {
		featureSnapshot[i] = Feature{Name: f.Name, Order: f.Order}
		if f.Deps != nil {
			featureSnapshot[i].Deps = append([]string(nil), f.Deps...)
		}
	}
	currentSnapshot := append([]string(nil), current...)

	_ = ValidateFeatureDeps(features)
	_ = ExpectedOrder(features, current)

	if !reflect.DeepEqual(features, featureSnapshot) {
		t.Fatalf("guard functions mutated features: got %+v, want %+v", features, featureSnapshot)
	}
	if !reflect.DeepEqual(current, currentSnapshot) {
		t.Fatalf("guard functions mutated current: got %v, want %v", current, currentSnapshot)
	}
}
