package server

import (
	"reflect"
	"sync"
	"testing"
)

// approvalOp 描述一次带确定性时间戳的审批决策，供表驱动测试按序重放。
type approvalOp struct {
	tool    string
	granted bool
	ms      int64
}

// replayApproval 把一组决策按序应用到审批板上。
func replayApproval(b *ApprovalBoard, ops []approvalOp) {
	for _, op := range ops {
		b.recordAt(op.tool, op.granted, op.ms)
	}
}

func TestApprovalBoardRecordAndSnapshot(t *testing.T) {
	tests := []struct {
		name string
		ops  []approvalOp
		want map[string]ApprovalMetric
	}{
		{
			name: "zero value is empty non-nil map",
			want: map[string]ApprovalMetric{},
		},
		{
			name: "single tool accumulates granted and denied",
			ops: []approvalOp{
				{tool: "shell", granted: true, ms: 1000},
				{tool: "shell", granted: true, ms: 2000},
				{tool: "shell", granted: false, ms: 3000},
			},
			want: map[string]ApprovalMetric{
				"shell": {Tool: "shell", Asked: 3, Granted: 2, Denied: 1, LastMS: 3000},
			},
		},
		{
			name: "multiple tools accumulate independently",
			ops: []approvalOp{
				{tool: "shell", granted: true, ms: 10},
				{tool: "write_file", granted: false, ms: 20},
				{tool: "shell", granted: false, ms: 30},
				{tool: "write_file", granted: false, ms: 40},
				{tool: "shell", granted: true, ms: 50},
			},
			want: map[string]ApprovalMetric{
				"shell":      {Tool: "shell", Asked: 3, Granted: 2, Denied: 1, LastMS: 50},
				"write_file": {Tool: "write_file", Asked: 2, Granted: 0, Denied: 2, LastMS: 40},
			},
		},
		{
			name: "CJK tool names preserved",
			ops: []approvalOp{
				{tool: "写入文件", granted: true, ms: 7},
				{tool: "执行命令", granted: false, ms: 8},
				{tool: "写入文件", granted: true, ms: 9},
			},
			want: map[string]ApprovalMetric{
				"写入文件": {Tool: "写入文件", Asked: 2, Granted: 2, Denied: 0, LastMS: 9},
				"执行命令": {Tool: "执行命令", Asked: 1, Granted: 0, Denied: 1, LastMS: 8},
			},
		},
		{
			name: "empty tool name allowed",
			ops: []approvalOp{
				{tool: "", granted: false, ms: 1},
			},
			want: map[string]ApprovalMetric{
				"": {Tool: "", Asked: 1, Granted: 0, Denied: 1, LastMS: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b ApprovalBoard
			replayApproval(&b, tt.ops)
			if got := b.Snapshot(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Snapshot() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApprovalBoardSnapshotIsolation(t *testing.T) {
	var b ApprovalBoard
	replayApproval(&b, []approvalOp{{tool: "shell", granted: true, ms: 100}})

	snap := b.Snapshot()

	// 修改返回值（覆盖条目、新增条目）不得影响内部状态。
	snap["shell"] = ApprovalMetric{Tool: "shell", Asked: 99, Granted: 99, Denied: 99, LastMS: 99}
	snap["injected"] = ApprovalMetric{Tool: "injected", Asked: 1}
	want := map[string]ApprovalMetric{
		"shell": {Tool: "shell", Asked: 1, Granted: 1, Denied: 0, LastMS: 100},
	}
	if got := b.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改 Snapshot 影响了内部状态: %#v", got)
	}

	// 两次 Snapshot 之间也必须互不影响。
	if got := b.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("后续 Snapshot() = %#v, want %#v", got, want)
	}
}

func TestApprovalBoardSummary(t *testing.T) {
	tests := []struct {
		name string
		ops  []approvalOp
		want string
	}{
		{
			name: "zero value",
			want: "审批决策汇总：暂无记录",
		},
		{
			name: "single tool",
			ops: []approvalOp{
				{tool: "shell", granted: true, ms: 1000},
				{tool: "shell", granted: false, ms: 2000},
			},
			want: "审批决策汇总（共 2 次询问，授予 1 次，拒绝 1 次）\n" +
				"  shell：询问 2 次，授予 1 次，拒绝 1 次，最后决策于 2000ms",
		},
		{
			name: "multiple tools sorted by name regardless of insert order",
			ops: []approvalOp{
				{tool: "zeta", granted: true, ms: 1},
				{tool: "alpha", granted: false, ms: 2},
				{tool: "mid", granted: true, ms: 3},
			},
			want: "审批决策汇总（共 3 次询问，授予 2 次，拒绝 1 次）\n" +
				"  alpha：询问 1 次，授予 0 次，拒绝 1 次，最后决策于 2ms\n" +
				"  mid：询问 1 次，授予 1 次，拒绝 0 次，最后决策于 3ms\n" +
				"  zeta：询问 1 次，授予 1 次，拒绝 0 次，最后决策于 1ms",
		},
		{
			name: "CJK tool names sorted deterministically",
			ops: []approvalOp{
				{tool: "执行命令", granted: true, ms: 11},
				{tool: "写入文件", granted: false, ms: 22},
			},
			want: "审批决策汇总（共 2 次询问，授予 1 次，拒绝 1 次）\n" +
				"  写入文件：询问 1 次，授予 0 次，拒绝 1 次，最后决策于 22ms\n" +
				"  执行命令：询问 1 次，授予 1 次，拒绝 0 次，最后决策于 11ms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b ApprovalBoard
			replayApproval(&b, tt.ops)
			if got := b.Summary(); got != tt.want {
				t.Fatalf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApprovalBoardSummaryStable(t *testing.T) {
	ops := []approvalOp{
		{tool: "zeta", granted: true, ms: 5},
		{tool: "alpha", granted: false, ms: 6},
		{tool: "消息", granted: true, ms: 7},
	}

	var a, c ApprovalBoard
	replayApproval(&a, ops)
	// 不同插入顺序、相同最终状态。
	replayApproval(&c, []approvalOp{ops[2], ops[0], ops[1]})

	first := a.Summary()
	second := a.Summary()
	otherOrder := c.Summary()
	if first != second {
		t.Fatalf("Summary() 不稳定: 第一次 %q, 第二次 %q", first, second)
	}
	if first != otherOrder {
		t.Fatalf("不同插入顺序产生不同汇总: %q vs %q", first, otherOrder)
	}
}

func TestApprovalBoardZeroValue(t *testing.T) {
	var b ApprovalBoard

	// 零值上所有方法都必须安全可用。
	if snap := b.Snapshot(); snap == nil || len(snap) != 0 {
		t.Fatalf("零值 Snapshot() = %#v, want 空非 nil 映射", snap)
	}
	if got := b.Summary(); got != "审批决策汇总：暂无记录" {
		t.Fatalf("零值 Summary() = %q", got)
	}
	b.Record("shell", true)
	want := map[string]ApprovalMetric{
		"shell": {Tool: "shell", Asked: 1, Granted: 1, Denied: 0},
	}
	got := b.Snapshot()
	m := got["shell"]
	if m.Asked != want["shell"].Asked || m.Granted != want["shell"].Granted || m.Denied != want["shell"].Denied {
		t.Fatalf("零值 Record() 后 Snapshot() = %#v, want %#v", got, want)
	}
	if m.LastMS <= 0 {
		t.Fatalf("Record() 未记录时间戳: LastMS = %d", m.LastMS)
	}
}

func TestApprovalBoardConcurrent(t *testing.T) {
	var b ApprovalBoard
	const (
		workers    = 8
		iterations = 300
	)
	tools := []string{"shell", "write_file", "写入文件"}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				tool := tools[(id+i)%len(tools)]
				b.Record(tool, (id+i)%2 == 0)
				snap := b.Snapshot()
				if snap == nil {
					t.Errorf("并发 Snapshot() 返回 nil")
					return
				}
				if summary := b.Summary(); len(summary) == 0 {
					t.Errorf("并发 Summary() 返回空串")
					return
				}
			}
		}(w)
	}
	wg.Wait()

	snap := b.Snapshot()
	var totalAsked, totalGranted, totalDenied int
	for _, m := range snap {
		if m.Asked != m.Granted+m.Denied {
			t.Errorf("工具 %q 计数不守恒: Asked=%d, Granted=%d, Denied=%d", m.Tool, m.Asked, m.Granted, m.Denied)
		}
		if m.LastMS <= 0 {
			t.Errorf("工具 %q 缺少 LastMS", m.Tool)
		}
		totalAsked += m.Asked
		totalGranted += m.Granted
		totalDenied += m.Denied
	}
	if want := workers * iterations; totalAsked != want {
		t.Fatalf("总询问次数 = %d, want %d", totalAsked, want)
	}
	if totalGranted+totalDenied != totalAsked {
		t.Fatalf("总计不守恒: Asked=%d, Granted=%d, Denied=%d", totalAsked, totalGranted, totalDenied)
	}
	if len(snap) != len(tools) {
		t.Fatalf("工具数 = %d, want %d: %#v", len(snap), len(tools), snap)
	}
}
