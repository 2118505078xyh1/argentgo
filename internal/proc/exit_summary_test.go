package proc

import (
	"reflect"
	"sync"
	"testing"
)

// 覆盖正常分类、超时优先级、长度不一致以及各种边界输入。
func TestSummarizeExits(t *testing.T) {
	tests := []struct {
		name     string
		codes    []int
		timeouts []bool
		want     map[string]int
	}{
		{
			name:     "nil inputs yield all zero counts",
			codes:    nil,
			timeouts: nil,
			want:     map[string]int{"success": 0, "failure": 0, "timeout": 0},
		},
		{
			name:     "empty slices yield all zero counts",
			codes:    []int{},
			timeouts: []bool{},
			want:     map[string]int{"success": 0, "failure": 0, "timeout": 0},
		},
		{
			name:     "all successes",
			codes:    []int{0, 0, 0},
			timeouts: []bool{false, false, false},
			want:     map[string]int{"success": 3, "failure": 0, "timeout": 0},
		},
		{
			name:     "all failures with varied nonzero codes",
			codes:    []int{1, 2, 130, 124, -1, 42},
			timeouts: []bool{false, false, false, false, false, false},
			want:     map[string]int{"success": 0, "failure": 6, "timeout": 0},
		},
		{
			name:     "all timeouts regardless of exit code",
			codes:    []int{0, 1, -1},
			timeouts: []bool{true, true, true},
			want:     map[string]int{"success": 0, "failure": 0, "timeout": 3},
		},
		{
			name:     "timeout flag wins over success code",
			codes:    []int{0},
			timeouts: []bool{true},
			want:     map[string]int{"success": 0, "failure": 0, "timeout": 1},
		},
		{
			name:     "mixed outcomes",
			codes:    []int{0, 1, 0, 7, 0},
			timeouts: []bool{false, false, true, false, false},
			want:     map[string]int{"success": 2, "failure": 2, "timeout": 1},
		},
		{
			name:     "timeout flag wins over failure code",
			codes:    []int{9},
			timeouts: []bool{true},
			want:     map[string]int{"success": 0, "failure": 0, "timeout": 1},
		},
		{
			name:     "short timeouts missing flags default to false",
			codes:    []int{0, 1, 0},
			timeouts: []bool{false},
			want:     map[string]int{"success": 2, "failure": 1, "timeout": 0},
		},
		{
			name:     "short timeouts still honor present flags",
			codes:    []int{0, 1, 0},
			timeouts: []bool{true, false},
			want:     map[string]int{"success": 1, "failure": 1, "timeout": 1},
		},
		{
			name:     "long timeouts extra flags are ignored",
			codes:    []int{0, 1},
			timeouts: []bool{false, false, true, true},
			want:     map[string]int{"success": 1, "failure": 1, "timeout": 0},
		},
		{
			name:     "nil timeouts are treated as all false",
			codes:    []int{0, 1, 0},
			timeouts: nil,
			want:     map[string]int{"success": 2, "failure": 1, "timeout": 0},
		},
		{
			name:     "empty timeouts with codes are treated as all false",
			codes:    []int{0, 1},
			timeouts: []bool{},
			want:     map[string]int{"success": 1, "failure": 1, "timeout": 0},
		},
		{
			name:     "empty codes ignore timeout flags",
			codes:    []int{},
			timeouts: []bool{true, true},
			want:     map[string]int{"success": 0, "failure": 0, "timeout": 0},
		},
		{
			// CJK 码点只是普通整数，不应被特殊处理；函数不解码任何文本。
			name:     "codes equal to CJK code points are ordinary integers",
			codes:    []int{20013, 25991}, // '中'、'文' 的 Unicode 码点
			timeouts: []bool{false, false},
			want:     map[string]int{"success": 0, "failure": 2, "timeout": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeExits(tt.codes, tt.timeouts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("summarizeExits(%v, %v) = %v, want %v", tt.codes, tt.timeouts, got, tt.want)
			}
		})
	}
}

// 结果形状必须稳定：无论输入如何，三个键始终存在，且只含这三个键。
func TestSummarizeExitsAlwaysHasAllKeys(t *testing.T) {
	inputs := []struct {
		codes    []int
		timeouts []bool
	}{
		{nil, nil},
		{[]int{}, nil},
		{[]int{0}, []bool{false}},
		{[]int{1}, nil},
		{[]int{2}, []bool{true}},
		{[]int{0, 1, 2}, []bool{false, false, true}},
	}
	for _, in := range inputs {
		got := summarizeExits(in.codes, in.timeouts)
		if len(got) != 3 {
			t.Fatalf("summarizeExits(%v, %v) 含 %d 个键，期望恰好 3 个：%v", in.codes, in.timeouts, len(got), got)
		}
		for _, key := range []string{exitSummarySuccess, exitSummaryFailure, exitSummaryTimeout} {
			if _, ok := got[key]; !ok {
				t.Fatalf("summarizeExits(%v, %v) 缺少键 %q：%v", in.codes, in.timeouts, key, got)
			}
		}
	}
}

// 同一输入的计数必须逐次一致，不依赖 map 的迭代顺序。
func TestSummarizeExitsIsStable(t *testing.T) {
	codes := []int{0, 1, 0, 7, -3, 0}
	timeouts := []bool{false, false, true, false, false, false}
	want := summarizeExits(codes, timeouts)
	for i := 0; i < 50; i++ {
		if got := summarizeExits(codes, timeouts); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次调用返回 %v，期望稳定的 %v", i, got, want)
		}
	}
}

// 纯函数可被并发调用；-race 会验证它没有共享可变状态。
func TestSummarizeExitsConcurrent(t *testing.T) {
	cases := []struct {
		codes    []int
		timeouts []bool
		want     map[string]int
	}{
		{[]int{0, 0}, []bool{false, false}, map[string]int{"success": 2, "failure": 0, "timeout": 0}},
		{[]int{1, 2, 3}, nil, map[string]int{"success": 0, "failure": 3, "timeout": 0}},
		{[]int{0, 1}, []bool{true, true}, map[string]int{"success": 0, "failure": 0, "timeout": 2}},
		{nil, []bool{true}, map[string]int{"success": 0, "failure": 0, "timeout": 0}},
	}
	var wg sync.WaitGroup
	for range 20 {
		for _, tc := range cases {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if got := summarizeExits(tc.codes, tc.timeouts); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("summarizeExits(%v, %v) = %v, want %v", tc.codes, tc.timeouts, got, tc.want)
				}
			}()
		}
	}
	wg.Wait()
}
