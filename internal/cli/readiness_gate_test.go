package cli

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestAggregateReadinessAllReady(t *testing.T) {
	// 所有组件就绪时判定为 ready，且没有未就绪项。
	checks := []readinessCheck{
		{name: readinessComponentConfig, ready: true},
		{name: readinessComponentProvider, ready: true},
		{name: readinessComponentWorkspace, ready: true},
	}
	got := aggregateReadiness(checks)
	if !got.ready {
		t.Fatal("ready = false, want true when all components are ready")
	}
	if len(got.pending) != 0 {
		t.Fatalf("pending = %+v, want empty", got.pending)
	}
	if s := got.String(); s != "ready" {
		t.Fatalf("String() = %q, want %q", s, "ready")
	}
}

func TestAggregateReadinessEmptyList(t *testing.T) {
	// 空清单没有需要等待的组件，视为就绪；nil 与空切片结果应一致。
	fromNil := aggregateReadiness(nil)
	if !fromNil.ready {
		t.Fatal("ready = false, want true for nil checks")
	}
	if len(fromNil.pending) != 0 {
		t.Fatalf("pending = %+v, want empty", fromNil.pending)
	}
	fromEmpty := aggregateReadiness([]readinessCheck{})
	if !reflect.DeepEqual(fromNil, fromEmpty) {
		t.Fatalf("nil and empty slice differ: %+v vs %+v", fromNil, fromEmpty)
	}
}

func TestAggregateReadinessPartiallyNotReady(t *testing.T) {
	// 按 table-driven 覆盖部分未就绪：单项、多项保序、全部未就绪与字段留空。
	cases := []struct {
		name        string
		checks      []readinessCheck
		wantReady   bool
		wantPending []notReadyItem
	}{
		{
			name: "one not ready",
			checks: []readinessCheck{
				{name: readinessComponentConfig, ready: true},
				{name: readinessComponentProvider, ready: false, reason: "未配置 API key"},
			},
			wantReady: false,
			wantPending: []notReadyItem{
				{component: readinessComponentProvider, reason: "未配置 API key"},
			},
		},
		{
			name: "multiple not ready preserve input order",
			checks: []readinessCheck{
				{name: readinessComponentConfig, ready: false, reason: "配置文件解析失败"},
				{name: readinessComponentProvider, ready: true},
				{name: readinessComponentWorkspace, ready: false, reason: "目录不可写"},
			},
			wantReady: false,
			wantPending: []notReadyItem{
				{component: readinessComponentConfig, reason: "配置文件解析失败"},
				{component: readinessComponentWorkspace, reason: "目录不可写"},
			},
		},
		{
			name: "all not ready",
			checks: []readinessCheck{
				{name: readinessComponentConfig, ready: false},
				{name: readinessComponentProvider, ready: false, reason: "超时"},
			},
			wantReady: false,
			wantPending: []notReadyItem{
				{component: readinessComponentConfig},
				{component: readinessComponentProvider, reason: "超时"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := aggregateReadiness(tc.checks)
			if got.ready != tc.wantReady {
				t.Fatalf("ready = %v, want %v", got.ready, tc.wantReady)
			}
			if !reflect.DeepEqual(got.pending, tc.wantPending) {
				t.Fatalf("pending = %+v, want %+v", got.pending, tc.wantPending)
			}
		})
	}
}

func TestAggregateReadinessDoesNotMutateInput(t *testing.T) {
	// 聚合器只读 checks，不能改动调用方的切片元素。
	checks := []readinessCheck{
		{name: readinessComponentConfig, ready: false, reason: "x"},
		{name: readinessComponentProvider, ready: true},
	}
	before := append([]readinessCheck(nil), checks...)
	_ = aggregateReadiness(checks)
	if !reflect.DeepEqual(checks, before) {
		t.Fatalf("aggregateReadiness mutated input: got %+v, want %+v", checks, before)
	}
}

func TestAggregateReadinessStringSummary(t *testing.T) {
	// 摘要应逐字节稳定、合法 UTF-8，且中文原因不被切断。
	cases := []struct {
		name   string
		checks []readinessCheck
		want   string
	}{
		{
			name:   "ready",
			checks: []readinessCheck{{name: readinessComponentConfig, ready: true}},
			want:   "ready",
		},
		{
			name:   "single pending",
			checks: []readinessCheck{{name: readinessComponentConfig, ready: false, reason: "解析失败"}},
			want:   "未就绪：config：解析失败",
		},
		{
			name: "multiple pending",
			checks: []readinessCheck{
				{name: readinessComponentConfig, ready: false, reason: "解析失败"},
				{name: readinessComponentProvider, ready: false, reason: "超时"},
			},
			want: "未就绪：config：解析失败, provider：超时",
		},
		{
			name:   "empty reason",
			checks: []readinessCheck{{name: readinessComponentWorkspace, ready: false}},
			want:   "未就绪：workspace",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := aggregateReadiness(tc.checks).String()
			if got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("String() = %q is not valid UTF-8", got)
			}
		})
	}
}
