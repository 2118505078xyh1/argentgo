package pluginapi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTopoSortSuccess(t *testing.T) {
	tests := []struct {
		name     string
		features []Feature
		want     []string
	}{
		{
			name: "linear chain",
			features: []Feature{
				{Name: "c", Deps: []string{"b"}, Order: 0},
				{Name: "a", Deps: nil, Order: 0},
				{Name: "b", Deps: []string{"a"}, Order: 0},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "same layer tie-break by order then name",
			features: []Feature{
				{Name: "delta", Order: 2},
				{Name: "alpha", Order: 1},
				{Name: "charlie", Order: 1},
				{Name: "bravo", Order: 0},
			},
			want: []string{"bravo", "alpha", "charlie", "delta"},
		},
		{
			name: "later layer does not jump ahead of earlier layer",
			features: []Feature{
				{Name: "c", Order: 2},                      // 无依赖，位于第 0 层
				{Name: "b", Deps: []string{"a"}, Order: 1}, // 依赖 a，位于第 1 层
				{Name: "a", Order: 0},
			},
			// 分层语义：同一层才按 (Order, Name) 排序，b 虽 Order 更小但属于后一层。
			want: []string{"a", "c", "b"},
		},
		{
			name: "diamond",
			features: []Feature{
				{Name: "d", Deps: []string{"b", "c"}, Order: 0},
				{Name: "c", Deps: []string{"a"}, Order: 1},
				{Name: "b", Deps: []string{"a"}, Order: 0},
				{Name: "a", Order: 0},
			},
			want: []string{"a", "b", "c", "d"},
		},
		{
			name: "duplicated dep is deduplicated",
			features: []Feature{
				{Name: "b", Deps: []string{"a", "a"}, Order: 0},
				{Name: "a", Order: 0},
			},
			want: []string{"a", "b"},
		},
		{
			name:     "empty input",
			features: nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TopoSort(tt.features)
			if err != nil {
				t.Fatalf("TopoSort() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("TopoSort() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTopoSortErrors(t *testing.T) {
	tests := []struct {
		name     string
		features []Feature
		wantErr  error
		wantPath string // 期望出现在错误文本中的路径片段
	}{
		{
			name:     "self loop",
			features: []Feature{{Name: "a", Deps: []string{"a"}}},
			wantErr:  ErrCycle,
			wantPath: "a -> a",
		},
		{
			name: "two node cycle",
			features: []Feature{
				{Name: "a", Deps: []string{"b"}},
				{Name: "b", Deps: []string{"a"}},
			},
			wantErr:  ErrCycle,
			wantPath: "a -> b -> a",
		},
		{
			name: "long cycle carries full path",
			features: []Feature{
				{Name: "d", Deps: []string{"a"}},
				{Name: "b", Deps: []string{"c"}},
				{Name: "c", Deps: []string{"d"}},
				{Name: "a", Deps: []string{"b"}},
			},
			wantErr:  ErrCycle,
			wantPath: "a -> b -> c -> d -> a",
		},
		{
			name: "missing dependency",
			features: []Feature{
				{Name: "a", Deps: []string{"ghost"}},
			},
			wantErr:  ErrMissingDependency,
			wantPath: `"a" -> "ghost"`,
		},
		{
			name: "duplicate feature",
			features: []Feature{
				{Name: "a", Order: 0},
				{Name: "a", Order: 1},
			},
			wantErr:  ErrDuplicateFeature,
			wantPath: `"a"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TopoSort(tt.features)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("TopoSort() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
			if got != nil {
				t.Fatalf("TopoSort() result = %v, want nil on error", got)
			}
			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Errorf("error %q does not contain path %q", err, tt.wantPath)
			}
		})
	}
}

func TestTopoSortShuffledInputSameOutput(t *testing.T) {
	base := []Feature{
		{Name: "root", Order: 0},
		{Name: "z", Deps: []string{"root"}, Order: 5},
		{Name: "m", Deps: []string{"root"}, Order: 1},
		{Name: "n", Deps: []string{"root"}, Order: 1},
		{Name: "leaf", Deps: []string{"z", "m", "n"}, Order: 0},
	}
	want, err := TopoSort(base)
	if err != nil {
		t.Fatal(err)
	}

	shuffled := []Feature{
		{Name: "leaf", Deps: []string{"z", "m", "n"}, Order: 0},
		{Name: "n", Deps: []string{"root"}, Order: 1},
		{Name: "root", Order: 0},
		{Name: "m", Deps: []string{"root"}, Order: 1},
		{Name: "z", Deps: []string{"root"}, Order: 5},
	}
	got, err := TopoSort(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shuffled input produced %v, want %v", got, want)
	}
}

func TestTopoSortDoesNotMutateInput(t *testing.T) {
	features := []Feature{
		{Name: "b", Deps: []string{"a", "a"}, Order: 2},
		{Name: "a", Deps: nil, Order: 1},
	}
	// 深拷贝一份快照，Deps 单独复制，便于精确比较。
	snapshot := make([]Feature, len(features))
	for i, f := range features {
		snapshot[i] = Feature{Name: f.Name, Order: f.Order}
		if f.Deps != nil {
			snapshot[i].Deps = append([]string(nil), f.Deps...)
		}
	}

	got, err := TopoSort(features)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("TopoSort() = %v, want [a b]", got)
	}
	if !reflect.DeepEqual(features, snapshot) {
		t.Fatalf("TopoSort mutated input: got %+v, want %+v", features, snapshot)
	}
}
