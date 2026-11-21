package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// discardTS 是丢弃测试使用的固定时间戳。
var discardTS = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

func discardRecord(id, parent, kind, data string) Record {
	return Record{ID: id, Parent: parent, TS: discardTS, Kind: kind, Data: json.RawMessage(data)}
}

type discardFixture struct {
	path    string
	session *Session
	before  []byte
}

// newDiscardFixture 把记录写成真实的 JSONL 转录并重新打开会话，
// 使分叉与活动路径判定走与生产一致的文件路径。
func newDiscardFixture(t *testing.T, id string, records []Record) *discardFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, id+fileExt)
	writeRawSession(t, path, records)
	session, err := OpenSession(dir, id)
	if err != nil {
		t.Fatalf("OpenSession(%q): %v", id, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return &discardFixture{path: path, session: session, before: before}
}

func (f *discardFixture) assertSourceUnchanged(t *testing.T) {
	t.Helper()
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(after, f.before) {
		t.Fatalf("源会话文件被修改：\nbefore=%q\nafter =%q", f.before, after)
	}
}

// prodFailureTree 构造一棵生产式 n%06d 会话树：分叉点 n000003 之后是失败尝试
// n000004（含一条 is_error 工具）；标题记录位于分叉点之前，避免 Fork 额外插入标题。
func prodFailureTree() []Record {
	return []Record{
		discardRecord("n000001", "", KindHeader, `{"title":"生产会话"}`),
		discardRecord("n000002", "n000001", KindTitle, `{"title":"生产会话"}`),
		discardRecord("n000003", "n000002", KindUser, `"继续任务"`),
		discardRecord("n000004", "n000003", KindAttempt, `{"reason":"initial"}`),
		discardRecord("n000005", "n000004", KindTool, `{"name":"bash","output":"boom","is_error":true}`),
		discardRecord("n000006", "n000005", KindAssistant, `"收尾"`),
	}
}

// TestDiscardFailedAttemptProductionIDs 验证生产 id 下丢弃可用、血缘可追溯、
// 失败记录不带入分叉，且源会话逐字节不变。
func TestDiscardFailedAttemptProductionIDs(t *testing.T) {
	f := newDiscardFixture(t, "discard", prodFailureTree())
	fork, err := f.session.DiscardFailedAttempt("n000004")
	if err != nil {
		t.Fatalf("DiscardFailedAttempt: %v", err)
	}
	if fork == nil {
		t.Fatal("fork is nil")
	}
	if fork.ID() == f.session.ID() {
		t.Fatal("fork 复用了源会话 id")
	}
	f.assertSourceUnchanged(t)

	branches, err := Branches(fork.path())
	if err != nil {
		t.Fatalf("Branches(fork): %v", err)
	}
	var lineage *Branch
	for i := range branches {
		if branches[i].Kind == KindAttempt && branches[i].SourceSession == f.session.ID() {
			lineage = &branches[i]
			break
		}
	}
	if lineage == nil {
		t.Fatalf("血缘记录未出现在 Branches 中: %+v", branches)
	}
	if lineage.SourceRecord != "n000004" {
		t.Fatalf("血缘 SourceRecord = %q, want %q", lineage.SourceRecord, "n000004")
	}
	if !strings.Contains(lineage.Label, AttemptRetried) {
		t.Fatalf("血缘 Label = %q, want 包含 %q", lineage.Label, AttemptRetried)
	}

	forkRecords, err := fork.Records()
	if err != nil {
		t.Fatalf("fork.Records: %v", err)
	}
	for _, record := range forkRecords {
		if record.Kind == KindTool {
			t.Fatalf("失败尝试的工具记录被带进 fork: %+v", record)
		}
	}
	attempts, err := fork.Attempts()
	if err != nil {
		t.Fatalf("fork.Attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("fork 尝试记录数 = %d, want 1: %+v", len(attempts), attempts)
	}
	if attempts[0].Reason != AttemptRetried || attempts[0].SourceRecord != "n000004" {
		t.Fatalf("fork 血缘载荷 = %+v, want reason=%q source=%q", attempts[0], AttemptRetried, "n000004")
	}
}

// TestDiscardFailedAttemptRejectsSuccess 覆盖 fail-closed：尝试里混入成功工具结果时拒绝。
func TestDiscardFailedAttemptRejectsSuccess(t *testing.T) {
	records := prodFailureTree()
	records = append(records,
		discardRecord("n000007", "n000006", KindTool, `{"name":"bash","output":"ok","is_error":false}`),
	)
	f := newDiscardFixture(t, "discard", records)
	fork, err := f.session.DiscardFailedAttempt("n000004")
	if !errors.Is(err, ErrAttemptNotDiscardable) {
		t.Fatalf("err = %v, want ErrAttemptNotDiscardable", err)
	}
	if fork != nil {
		t.Fatal("含成功结果时不应返回 fork")
	}
	f.assertSourceUnchanged(t)
}

// TestDiscardFailedAttemptRejectsNoEvidence 覆盖无失败证据时拒绝。
func TestDiscardFailedAttemptRejectsNoEvidence(t *testing.T) {
	records := []Record{
		discardRecord("n000001", "", KindHeader, `{"title":"无失败"}`),
		discardRecord("n000002", "n000001", KindTitle, `{"title":"无失败"}`),
		discardRecord("n000003", "n000002", KindUser, `"继续"`),
		discardRecord("n000004", "n000003", KindAttempt, `{"reason":"initial"}`),
		discardRecord("n000005", "n000004", KindAssistant, `"done"`),
	}
	f := newDiscardFixture(t, "discard", records)
	fork, err := f.session.DiscardFailedAttempt("n000004")
	if !errors.Is(err, ErrAttemptNotDiscardable) {
		t.Fatalf("err = %v, want ErrAttemptNotDiscardable", err)
	}
	if fork != nil {
		t.Fatal("无失败证据时不应返回 fork")
	}
	f.assertSourceUnchanged(t)
}

// TestDiscardFailedAttemptRejectsCrossBranch 覆盖防跨分支：失败尝试的分叉点
// 不在活动路径上时必须拒绝，避免切到与当前对话无关的分支。
func TestDiscardFailedAttemptRejectsCrossBranch(t *testing.T) {
	records := []Record{
		discardRecord("n000001", "", KindHeader, `{"title":"跨分支"}`),
		discardRecord("n000002", "n000001", KindTitle, `{"title":"跨分支"}`),
		discardRecord("n000003", "n000002", KindUser, `"分支 A"`),
		discardRecord("n000004", "n000003", KindAttempt, `{"reason":"initial"}`),
		discardRecord("n000005", "n000004", KindTool, `{"is_error":true,"output":"boom"}`),
		discardRecord("n000006", "n000002", KindUser, `"分支 B"`),
		discardRecord("n000007", "n000006", KindAssistant, `"当前活动叶"`),
	}
	f := newDiscardFixture(t, "discard", records)
	fork, err := f.session.DiscardFailedAttempt("n000004")
	if !errors.Is(err, ErrAttemptBranchOffPath) {
		t.Fatalf("err = %v, want ErrAttemptBranchOffPath", err)
	}
	if fork != nil {
		t.Fatal("跨分支时不应返回 fork")
	}
	f.assertSourceUnchanged(t)
}

// TestDiscardFailedAttemptCJK 覆盖中文会话 id 与中文记录 id 的血缘写入和检索。
func TestDiscardFailedAttemptCJK(t *testing.T) {
	records := []Record{
		discardRecord("根", "", KindHeader, `{"title":"会话"}`),
		discardRecord("标题", "根", KindTitle, `{"title":"会话"}`),
		discardRecord("用户", "标题", KindUser, `"继续"`),
		discardRecord("尝试-甲", "用户", KindAttempt, `{"reason":"initial"}`),
		discardRecord("n000001", "尝试-甲", KindTool, `{"name":"编译","is_error":true,"output":"失败"}`),
	}
	f := newDiscardFixture(t, "会话", records)
	fork, err := f.session.DiscardFailedAttempt("尝试-甲")
	if err != nil {
		t.Fatalf("DiscardFailedAttempt: %v", err)
	}
	f.assertSourceUnchanged(t)

	branches, err := Branches(fork.path())
	if err != nil {
		t.Fatalf("Branches(fork): %v", err)
	}
	found := false
	for _, branch := range branches {
		if branch.Kind == KindAttempt && branch.SourceSession == "会话" && branch.SourceRecord == "尝试-甲" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("未找到 CJK 血缘记录: %+v", branches)
	}
}
