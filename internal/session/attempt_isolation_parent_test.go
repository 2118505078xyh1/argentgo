package session

import (
	"reflect"
	"testing"
)

// TestMarkFailureProductionIDs 覆盖生产转录的 n%06d id：工具记录不带尝试前缀，
// 必须靠 parent 边从 KindAttempt 根向下归属才能被识别。
func TestMarkFailureProductionIDs(t *testing.T) {
	records := []Record{
		isoRecord("n000001", "", KindHeader, `{"title":"生产会话"}`),
		isoRecord("n000002", "n000001", KindUser, `"继续任务"`),
		isoRecord("n000003", "n000002", KindAttempt, `{"reason":"initial"}`),
		isoRecord("n000004", "n000003", KindTool, `{"name":"bash","output":"boom","is_error":true}`),
		isoRecord("n000005", "n000004", KindTool, `{"name":"edit","output":"失败: 未定义的符号"}`),
	}
	got := MarkFailure(records, "n000003")
	want := []AttemptMarker{
		failedMarker("n000003", "n000003"),
		failedMarker("n000003", "n000004"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MarkFailure() = %#v, want %#v", got, want)
	}
	if !CanDiscardAttempt(got, "n000003") {
		t.Fatal("CanDiscardAttempt() = false, want true")
	}
	point, ok := BranchPointFor(records, "n000003")
	if point != "n000002" || !ok {
		t.Fatalf("BranchPointFor() = (%q, %v), want (%q, true)", point, ok, "n000002")
	}
}

// TestMarkFailureParentEdgePrefixBoundary 确认 parent 边归属不会吞掉 id 前缀相邻的
// 其它尝试：att-10 是另一棵子树，其工具记录不能被算进 att-1。
func TestMarkFailureParentEdgePrefixBoundary(t *testing.T) {
	records := []Record{
		isoRecord("n0", "", KindHeader, `{}`),
		isoRecord("att-1", "n0", KindAttempt, `{}`),
		isoRecord("n000001", "att-1", KindTool, `{"is_error":true,"output":"boom"}`),
		isoRecord("att-10", "n0", KindAttempt, `{}`),
		isoRecord("n000002", "att-10", KindTool, `{"is_error":true,"output":"boom"}`),
	}
	got := MarkFailure(records, "att-1")
	want := []AttemptMarker{failedMarker("att-1", "att-1")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MarkFailure() = %#v, want %#v", got, want)
	}
}

// TestMarkFailureCJKParentEdge 覆盖 CJK 尝试 id + 生产式工具 id 的 parent 边归属。
func TestMarkFailureCJKParentEdge(t *testing.T) {
	records := []Record{
		isoRecord("根", "", KindHeader, `{}`),
		isoRecord("尝试-甲", "根", KindAttempt, `{}`),
		isoRecord("n000001", "尝试-甲", KindTool, `{"name":"编译","is_error":true,"output":"失败"}`),
	}
	got := MarkFailure(records, "尝试-甲")
	want := []AttemptMarker{failedMarker("尝试-甲", "尝试-甲")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MarkFailure() = %#v, want %#v", got, want)
	}
	point, ok := BranchPointFor(records, "尝试-甲")
	if point != "根" || !ok {
		t.Fatalf("BranchPointFor() = (%q, %v), want (%q, true)", point, ok, "根")
	}
}

// TestMarkFailureSucceededToolBlocksDiscard 确认显式 is_error:false 的工具结果会被
// 标记成成功，从而让 CanDiscardAttempt 对混有成功结果的尝试 fail-closed。
func TestMarkFailureSucceededToolBlocksDiscard(t *testing.T) {
	records := []Record{
		isoRecord("n0", "", KindHeader, `{}`),
		isoRecord("att-1", "n0", KindAttempt, `{}`),
		isoRecord("att-1-t", "att-1", KindTool, `{"name":"bash","output":"boom","is_error":true}`),
		isoRecord("att-1-ok", "att-1", KindTool, `{"name":"bash","output":"ok","is_error":false}`),
	}
	got := MarkFailure(records, "att-1")
	if len(got) != 2 {
		t.Fatalf("MarkFailure() 长度 = %d, want 2: %#v", len(got), got)
	}
	want := []AttemptMarker{
		failedMarker("att-1", "att-1"),
		{AttemptID: "att-1", ParentID: "att-1", Outcome: attemptOutcomeSucceeded, TS: isoTS.Unix()},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MarkFailure() = %#v, want %#v", got, want)
	}
	if CanDiscardAttempt(got, "att-1") {
		t.Fatal("CanDiscardAttempt() = true, want false（含成功结果必须拒绝）")
	}
}
