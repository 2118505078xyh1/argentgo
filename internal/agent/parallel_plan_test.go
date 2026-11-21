package agent

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// batchIDs 按批次列出每批的工具 ID；每批只会填充 Parallel 或 Sequential 之一。
func batchIDs(batches []Batch) [][]string {
	out := make([][]string, 0, len(batches))
	for _, b := range batches {
		var ids []string
		for _, j := range b.Parallel {
			ids = append(ids, j.ID)
		}
		for _, j := range b.Sequential {
			ids = append(ids, j.ID)
		}
		out = append(out, ids)
	}
	return out
}

// flattenIDs 按批次顺序展开所有工具 ID，用于校验输入顺序稳定。
func flattenIDs(batches []Batch) []string {
	var ids []string
	for _, b := range batches {
		for _, j := range b.Parallel {
			ids = append(ids, j.ID)
		}
		for _, j := range b.Sequential {
			ids = append(ids, j.ID)
		}
	}
	return ids
}

func TestPlanBatchesMutatingAreSerial(t *testing.T) {
	jobs := []ToolJob{
		{ID: "1", Name: "write-a", Mutating: true},
		{ID: "2", Name: "write-b", Mutating: true},
	}
	got := PlanBatches(jobs, 4)
	want := []Batch{
		{Sequential: []ToolJob{{ID: "1", Name: "write-a", Mutating: true}}},
		{Sequential: []ToolJob{{ID: "2", Name: "write-b", Mutating: true}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batches = %+v, want %+v", got, want)
	}
	for i, b := range got {
		if len(b.Parallel) != 0 {
			t.Fatalf("batch %d has parallel jobs %+v, want none", i, b.Parallel)
		}
		if len(b.Sequential) != 1 {
			t.Fatalf("batch %d sequential = %+v, want exactly one mutating job", i, b.Sequential)
		}
	}
}

func TestPlanBatchesNonMutatingGrouping(t *testing.T) {
	jobs := []ToolJob{
		{ID: "1", Name: "a"}, {ID: "2", Name: "b"},
		{ID: "3", Name: "c"}, {ID: "4", Name: "d"},
		{ID: "5", Name: "e"},
	}
	got := PlanBatches(jobs, 2)
	want := [][]string{{"1", "2"}, {"3", "4"}, {"5"}}
	if ids := batchIDs(got); !reflect.DeepEqual(ids, want) {
		t.Fatalf("batch IDs = %v, want %v", ids, want)
	}
	for _, b := range got {
		if len(b.Sequential) != 0 {
			t.Fatalf("unexpected sequential jobs: %+v", b.Sequential)
		}
		if len(b.Parallel) == 0 || len(b.Parallel) > 2 {
			t.Fatalf("parallel chunk size = %d, want 1..2", len(b.Parallel))
		}
	}
}

func TestPlanBatchesMaxParallelBoundary(t *testing.T) {
	jobs := []ToolJob{
		{ID: "1", Name: "a"}, {ID: "2", Name: "b"}, {ID: "3", Name: "c"},
	}
	cases := []struct {
		name    string
		max     int
		wantIDs [][]string
		serial  bool
	}{
		{"negative", -1, [][]string{{"1"}, {"2"}, {"3"}}, true},
		{"zero", 0, [][]string{{"1"}, {"2"}, {"3"}}, true},
		{"one", 1, [][]string{{"1"}, {"2"}, {"3"}}, true},
		{"two", 2, [][]string{{"1", "2"}, {"3"}}, false},
		{"huge", 100, [][]string{{"1", "2", "3"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanBatches(jobs, tc.max)
			if ids := batchIDs(got); !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Fatalf("batch IDs = %v, want %v", ids, tc.wantIDs)
			}
			for _, b := range got {
				if tc.serial {
					if len(b.Parallel) != 0 || len(b.Sequential) != 1 {
						t.Fatalf("max=%d batch = %+v, want single sequential job", tc.max, b)
					}
				} else if len(b.Sequential) != 0 {
					t.Fatalf("max=%d unexpected sequential = %+v", tc.max, b.Sequential)
				}
			}
		})
	}
}

func TestPlanBatchesKeepsInputOrder(t *testing.T) {
	jobs := []ToolJob{
		{ID: "r1", Name: "read1"},
		{ID: "w1", Name: "write1", Mutating: true},
		{ID: "r2", Name: "read2"},
		{ID: "r3", Name: "read3"},
		{ID: "w2", Name: "write2", Mutating: true},
		{ID: "r4", Name: "read4"},
	}
	got := PlanBatches(jobs, 2)
	if want := []string{"r1", "w1", "r2", "r3", "w2", "r4"}; !reflect.DeepEqual(flattenIDs(got), want) {
		t.Fatalf("flatten = %v, want %v", flattenIDs(got), want)
	}
	if want := [][]string{{"r1"}, {"w1"}, {"r2", "r3"}, {"w2"}, {"r4"}}; !reflect.DeepEqual(batchIDs(got), want) {
		t.Fatalf("batches = %v, want %v", batchIDs(got), want)
	}
}

func TestPlanBatchesEmptyInput(t *testing.T) {
	if got := PlanBatches(nil, 4); len(got) != 0 {
		t.Fatalf("nil input batches = %+v, want empty", got)
	}
	if got := PlanBatches([]ToolJob{}, 4); len(got) != 0 {
		t.Fatalf("empty input batches = %+v, want empty", got)
	}
}

func TestPlanBatchesCJKNames(t *testing.T) {
	jobs := []ToolJob{
		{ID: "1", Name: "读取文件"},
		{ID: "2", Name: "写入配置", Mutating: true},
		{ID: "3", Name: "删除目录"},
	}
	got := PlanBatches(jobs, 2)
	want := []string{"读取文件", "写入配置", "删除目录"}
	var names []string
	for _, b := range got {
		for _, j := range b.Parallel {
			names = append(names, j.Name)
		}
		for _, j := range b.Sequential {
			names = append(names, j.Name)
		}
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for _, name := range names {
		if !utf8.ValidString(name) {
			t.Fatalf("invalid UTF-8 name: %q", name)
		}
	}
}

func TestPlanBatchesDoesNotMutateInput(t *testing.T) {
	jobs := []ToolJob{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b", Mutating: true},
		{ID: "3", Name: "c"},
	}
	before := append([]ToolJob(nil), jobs...)
	got := PlanBatches(jobs, 2)
	if !reflect.DeepEqual(jobs, before) {
		t.Fatalf("input mutated: %+v, want %+v", jobs, before)
	}
	// 改动返回值不应影响入参（输出切片与入参不共享底层数组）。
	if len(got) > 0 && len(got[0].Parallel) > 0 {
		got[0].Parallel[0].Name = "changed"
	}
	if !reflect.DeepEqual(jobs, before) {
		t.Fatalf("output aliases input: %+v, want %+v", jobs, before)
	}
}

func TestPlanBatchesDeterministic(t *testing.T) {
	jobs := []ToolJob{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b", Mutating: true},
		{ID: "3", Name: "c"},
		{ID: "4", Name: "d"},
	}
	first := PlanBatches(jobs, 2)
	for i := 0; i < 10; i++ {
		if got := PlanBatches(jobs, 2); !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d = %+v, want %+v", i, got, first)
		}
	}
}
