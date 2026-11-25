package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/session"
)

// checkpointWorkspace 准备一个含快照目标文件的独立工作区。
func checkpointWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// appendRawRecord 绕过会话句柄直接向转录追加一行，模拟外部写入产生的废弃分支：
// 文件顺序最后一条变了，但句柄内存中的活动叶节点仍指向追加前的记录。
func appendRawRecord(t *testing.T, sess *session.Session, id, parent, kind string) {
	t.Helper()
	path := filepath.Join(sess.Dir(), sess.ID()+".jsonl")
	line, err := json.Marshal(session.Record{
		ID: id, Parent: parent, Kind: kind, Data: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointCaptureUsesActiveLeafNotFileLast(t *testing.T) {
	root := checkpointWorkspace(t)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}

	// 活动分支：header -> A -> B -> C（活动叶节点为 C）。
	a, err := sess.AppendRecord(session.KindUser, map[string]any{"content": "A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(session.KindAssistant, map[string]any{"content": "B"}); err != nil {
		t.Fatal(err)
	}
	c, err := sess.AppendRecord(session.KindUser, map[string]any{"content": "C"})
	if err != nil {
		t.Fatal(err)
	}

	// 在废弃分支上追加一条记录，使其成为文件顺序最后一条；活动叶节点仍是 C。
	const abandoned = "abandoned-leaf"
	appendRawRecord(t, sess, abandoned, a.ID, session.KindAssistant)
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	if last := records[len(records)-1].ID; last != abandoned {
		t.Fatalf("测试前置条件失败：文件末条 = %q，期望废弃分支 %q", last, abandoned)
	}

	tracker := newCheckpointTracker(root, sess)
	tracker.capture(agent.Checkpoint{Paths: []string{"a.txt"}}, "prompt")
	if len(tracker.pending) != 1 {
		t.Fatalf("capture 产生 %d 个待处理检查点，期望 1", len(tracker.pending))
	}
	got := tracker.pending[0].Parent
	if got != c.ID {
		t.Fatalf("Parent = %q，期望活动叶节点 %q（而非文件末条 %q）", got, c.ID, abandoned)
	}
	if tracker.pending[0].Prompt != "prompt" || len(tracker.pending[0].Files) == 0 {
		t.Fatal("capture 的其它字段被意外改动")
	}

	// 落盘后从检查点重试，分叉点必须落在活动分支（含 C），不能切到废弃分支。
	tracker.finish(false)
	fork, err := sess.RetryFromCheckpoint("")
	if err != nil {
		t.Fatalf("RetryFromCheckpoint: %v", err)
	}
	forkRecords, err := fork.Records()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(forkRecords))
	for _, rec := range forkRecords {
		ids[rec.ID] = true
	}
	if !ids[c.ID] {
		t.Fatalf("重试分叉缺少活动叶节点 %q，分叉点错误", c.ID)
	}
	if ids[abandoned] {
		t.Fatal("重试分叉落在废弃分支上")
	}
}

func TestCheckpointCaptureLinearTreeUnchanged(t *testing.T) {
	root := checkpointWorkspace(t)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	last, err := sess.AppendRecord(session.KindUser, map[string]any{"content": "only"})
	if err != nil {
		t.Fatal(err)
	}
	// 线性树中文件末条即活动叶节点，行为必须与修复前一致。
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[len(records)-1].ID != last.ID {
		t.Fatal("测试前置条件失败：文件末条不是活动叶节点")
	}

	tracker := newCheckpointTracker(root, sess)
	tracker.capture(agent.Checkpoint{Paths: []string{"a.txt"}}, "prompt")
	if len(tracker.pending) != 1 {
		t.Fatalf("capture 产生 %d 个待处理检查点，期望 1", len(tracker.pending))
	}
	if got := tracker.pending[0].Parent; got != last.ID {
		t.Fatalf("线性树 Parent = %q，期望文件末条/活动叶节点 %q", got, last.ID)
	}
}
