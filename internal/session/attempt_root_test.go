package session

import (
	"errors"
	"testing"
)

// appendRootTool 在测试会话里写一条工具记录，is_error 决定它携带失败还是成功信号。
func appendRootTool(t *testing.T, s *Session, name, output string, isError bool) {
	t.Helper()
	if _, err := s.AppendRecord(KindTool, map[string]any{
		"name":     name,
		"output":   output,
		"is_error": isError,
	}); err != nil {
		t.Fatalf("AppendRecord(KindTool): %v", err)
	}
}

// lastRootRecordID 返回会话按文件顺序最后一条记录的 id。
func lastRootRecordID(t *testing.T, s *Session) string {
	t.Helper()
	records, err := s.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	return records[len(records)-1].ID
}

// TestBeginAttemptRootFirstIsDiscardable 覆盖方案 B 的正向路径：前置根先写，失败工具随后
// 以它为 parent，EndAttempt 的结果记录也落在同一子树内；MarkFailure 能命中，
// CanDiscardAttempt 为真，BranchPointFor 指向根之前的记录，DiscardFailedAttempt 成功。
func TestBeginAttemptRootFirstIsDiscardable(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "root first")
	if err := s.Append(KindUser, "task"); err != nil {
		t.Fatal(err)
	}
	branchPoint := lastRootRecordID(t, s)

	rootID, err := BeginAttempt(s, "task_start")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}
	if rootID == "" {
		t.Fatal("BeginAttempt 返回空 rootID")
	}
	if got := lastRootRecordID(t, s); got != rootID {
		t.Fatalf("写入后活动叶 = %q, want 根 %q", got, rootID)
	}

	appendRootTool(t, s, "bash", "boom", true)
	appendRootTool(t, s, "edit", "失败: 未定义的符号", true)
	if err := EndAttempt(s, rootID, "failed"); err != nil {
		t.Fatalf("EndAttempt: %v", err)
	}

	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	markers := MarkFailure(records, rootID)
	if len(markers) != 2 {
		t.Fatalf("MarkFailure 长度 = %d, want 2: %#v", len(markers), markers)
	}
	for _, marker := range markers {
		if marker.Outcome != AttemptFailed {
			t.Fatalf("标记结果 = %q, want failed: %#v", marker.Outcome, marker)
		}
	}
	if !CanDiscardAttempt(markers, rootID) {
		t.Fatal("CanDiscardAttempt = false, want true")
	}
	point, ok := BranchPointFor(records, rootID)
	if !ok || point != branchPoint {
		t.Fatalf("BranchPointFor = (%q, %v), want (%q, true)", point, ok, branchPoint)
	}
	fork, err := s.DiscardFailedAttempt(rootID)
	if err != nil {
		t.Fatalf("DiscardFailedAttempt: %v", err)
	}
	if fork == nil {
		t.Fatal("DiscardFailedAttempt 返回 nil 会话")
	}
}

// TestBeginAttemptMixedSuccessBlocksDiscard 确认前置根下混入一条 is_error:false 的工具后，
// 该成功标记会让 CanDiscardAttempt fail-closed，DiscardFailedAttempt 返回哨兵错误。
func TestBeginAttemptMixedSuccessBlocksDiscard(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "mixed")
	rootID, err := BeginAttempt(s, "task_start")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}
	appendRootTool(t, s, "bash", "boom", true)
	appendRootTool(t, s, "ls", "ok", false)
	if err := EndAttempt(s, rootID, "failed"); err != nil {
		t.Fatalf("EndAttempt: %v", err)
	}

	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	markers := MarkFailure(records, rootID)
	if len(markers) != 2 {
		t.Fatalf("MarkFailure 长度 = %d, want 2: %#v", len(markers), markers)
	}
	sawFailed, sawSucceeded := false, false
	for _, marker := range markers {
		switch marker.Outcome {
		case AttemptFailed:
			sawFailed = true
		case attemptOutcomeSucceeded:
			sawSucceeded = true
		default:
			t.Fatalf("意外标记结果 = %q", marker.Outcome)
		}
	}
	if !sawFailed || !sawSucceeded {
		t.Fatalf("标记结果不完整: failed=%v succeeded=%v", sawFailed, sawSucceeded)
	}
	if CanDiscardAttempt(markers, rootID) {
		t.Fatal("混入成功结果时 CanDiscardAttempt = true, want false")
	}
	if _, err := s.DiscardFailedAttempt(rootID); !errors.Is(err, ErrAttemptNotDiscardable) {
		t.Fatalf("DiscardFailedAttempt error = %v, want ErrAttemptNotDiscardable", err)
	}
}

// TestProductionAttemptTailFailsClosedWithoutBeginAttempt 锁定 R11 §2.3 事实：
// 不调用 BeginAttempt 时，生产转录把 KindAttempt 根写在运行末尾，失败工具是它的祖先
// 而非后代，MarkFailure 无法命中，因此 CanDiscardAttempt / DiscardFailedAttempt 都 fail-closed。
func TestProductionAttemptTailFailsClosedWithoutBeginAttempt(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "tail")
	if err := s.Append(KindUser, "task"); err != nil {
		t.Fatal(err)
	}
	// 失败工具写在运行过程中，随后才由 recordAttempt 在末尾写 KindAttempt 根。
	appendRootTool(t, s, "bash", "boom", true)
	appendRootTool(t, s, "edit", "失败: 符号未定义", true)
	if _, err := s.RecordAttemptError(AttemptFailed, errors.New("run failed")); err != nil {
		t.Fatalf("RecordAttemptError: %v", err)
	}

	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	tailID := records[len(records)-1].ID
	markers := MarkFailure(records, tailID)
	if len(markers) != 0 {
		t.Fatalf("生产尾部根的 MarkFailure = %#v, want 空（失败工具是祖先）", markers)
	}
	if CanDiscardAttempt(markers, tailID) {
		t.Fatal("生产尾部根 CanDiscardAttempt = true, want false")
	}
	if _, err := s.DiscardFailedAttempt(tailID); !errors.Is(err, ErrAttemptNotDiscardable) {
		t.Fatalf("DiscardFailedAttempt error = %v, want ErrAttemptNotDiscardable", err)
	}
}

// TestBeginAttemptDefaultReasonAndEndAttemptShape 校验默认原因、结果记录形态与 fail-closed 入参：
// EndAttempt 与生产 recordAttempt 一样以当前活动叶为 parent，结果记录不设置 SourceRecord
// （该字段专用于 retry/fork 血缘），非法入参不写入任何记录。
func TestBeginAttemptDefaultReasonAndEndAttemptShape(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "shape")

	if _, err := BeginAttempt(s, "   "); err != nil {
		t.Fatalf("BeginAttempt(空原因): %v", err)
	}
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	root := records[len(records)-1]
	if root.Kind != KindAttempt {
		t.Fatalf("根记录 kind = %q, want attempt", root.Kind)
	}
	var begin Attempt
	if err := jsonUnmarshal(root.Data, &begin); err != nil {
		t.Fatalf("解码根记录: %v", err)
	}
	if begin.Reason != attemptBeginReason {
		t.Fatalf("默认原因 = %q, want %q", begin.Reason, attemptBeginReason)
	}

	if err := EndAttempt(s, root.ID, "failed"); err != nil {
		t.Fatalf("EndAttempt: %v", err)
	}
	records, err = s.Records()
	if err != nil {
		t.Fatal(err)
	}
	verdict := records[len(records)-1]
	if verdict.Kind != KindAttempt || verdict.Parent != root.ID {
		t.Fatalf("结果记录 = (kind %q, parent %q), want (attempt, %q)", verdict.Kind, verdict.Parent, root.ID)
	}
	var result Attempt
	if err := jsonUnmarshal(verdict.Data, &result); err != nil {
		t.Fatalf("解码结果记录: %v", err)
	}
	if result.Reason != "failed" {
		t.Fatalf("结果原因 = %q, want failed", result.Reason)
	}
	if result.SourceRecord != "" {
		t.Fatalf("结果记录不应带 SourceRecord, got %q", result.SourceRecord)
	}

	before, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	count := len(before)
	for _, invalid := range []struct {
		name    string
		s       *Session
		rootID  string
		verdict string
	}{
		{"nil session", nil, root.ID, "failed"},
		{"empty root", s, "", "failed"},
		{"empty verdict", s, root.ID, "   "},
	} {
		if err := EndAttempt(invalid.s, invalid.rootID, invalid.verdict); err == nil {
			t.Fatalf("%s: EndAttempt 应拒绝非法入参", invalid.name)
		}
	}
	if _, err := BeginAttempt(nil, "task_start"); err == nil {
		t.Fatal("BeginAttempt(nil) 应返回错误")
	}
	after, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != count {
		t.Fatalf("非法入参写入了记录: %d -> %d", count, len(after))
	}
}
