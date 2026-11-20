package session

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppendProgressWritesRetrievableRecord(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "progress")
	stamp := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	want := Progress{
		Turn:          1,
		AssistantText: "partial answer",
		ToolCalls:     []string{"fs.read"},
		TS:            stamp,
	}
	if err := sess.AppendProgress(want); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}
	if got := sess.ProgressCount(); got != 1 {
		t.Fatalf("ProgressCount = %d, want 1", got)
	}

	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	if last := records[len(records)-1]; last.Kind != kindProgress {
		t.Fatalf("last record kind = %q, want %q", last.Kind, kindProgress)
	}

	got, ok := sess.LatestProgress()
	if !ok {
		t.Fatal("LatestProgress found no progress")
	}
	if got.Turn != want.Turn || got.AssistantText != want.AssistantText ||
		len(got.ToolCalls) != 1 || got.ToolCalls[0] != "fs.read" {
		t.Fatalf("LatestProgress = %+v, want %+v", got, want)
	}
	if got.TS.IsZero() {
		t.Fatal("LatestProgress lost its timestamp")
	}
	// Progress 是持久性记录，绝不能变成模型历史。
	for _, entry := range sess.Messages() {
		if entry.Content == want.AssistantText {
			t.Fatalf("progress leaked into model history: %+v", entry)
		}
	}
}

func TestLatestProgressReturnsNewestOnActiveBranch(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "progress newest")
	for i := 1; i <= 3; i++ {
		if err := sess.AppendProgress(Progress{
			Turn:          i,
			AssistantText: fmt.Sprintf("chunk %d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := sess.ProgressCount(); got != 3 {
		t.Fatalf("ProgressCount = %d, want 3", got)
	}
	got, ok := sess.LatestProgress()
	if !ok || got.Turn != 3 || got.AssistantText != "chunk 3" {
		t.Fatalf("LatestProgress = %+v, %v; want turn 3 / chunk 3", got, ok)
	}
}

func TestAppendProgressCapsAndRedactsText(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "progress redaction")
	const secret = "2118505078xyh1"
	toolCalls := make([]string, maxProgressToolCalls+5)
	for i := range toolCalls {
		toolCalls[i] = "tool-" + strconv.Itoa(i)
	}
	toolCalls[0] = "read token = " + secret
	input := Progress{
		Turn:          2,
		AssistantText: "token = " + secret + "\n" + strings.Repeat("界", maxProgressTextRunes+64),
		ToolCalls:     toolCalls,
	}
	if err := sess.AppendProgress(input); err != nil {
		t.Fatal(err)
	}

	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	raw := string(records[len(records)-1].Data)
	if strings.Contains(raw, secret) {
		t.Fatalf("stored progress still contains the secret: %s", raw)
	}
	if !strings.Contains(raw, "[redacted:") {
		t.Fatalf("stored progress was not redacted: %s", raw)
	}

	got, ok := sess.LatestProgress()
	if !ok {
		t.Fatal("LatestProgress found no progress")
	}
	if n := len([]rune(got.AssistantText)); n != maxProgressTextRunes {
		t.Fatalf("assistant text length = %d runes, want %d", n, maxProgressTextRunes)
	}
	if len(got.ToolCalls) != maxProgressToolCalls {
		t.Fatalf("tool calls = %d, want %d", len(got.ToolCalls), maxProgressToolCalls)
	}
	if strings.Contains(got.ToolCalls[0], secret) || !strings.Contains(got.ToolCalls[0], "[redacted:") {
		t.Fatalf("tool call not redacted: %q", got.ToolCalls[0])
	}
}

func TestRecoverProgressRejectsStaleProgress(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "progress stale")
	if err := sess.AppendProgress(Progress{Turn: 1, AssistantText: "partial"}); err != nil {
		t.Fatal(err)
	}
	got, ok := RecoverProgress(sess)
	if !ok || got.Turn != 1 {
		t.Fatalf("in-flight progress rejected: %+v, %v", got, ok)
	}

	// 提交该轮次的 assistant 消息会使其进度过时。
	if err := sess.Append(KindAssistant, "complete answer"); err != nil {
		t.Fatal(err)
	}
	if got, ok := RecoverProgress(sess); ok {
		t.Fatalf("stale progress accepted after assistant commit: %+v", got)
	}

	// 下一轮次的检查点重新有效。
	if err := sess.AppendProgress(Progress{Turn: 2, AssistantText: "next partial"}); err != nil {
		t.Fatal(err)
	}
	got, ok = RecoverProgress(sess)
	if !ok || got.Turn != 2 || got.AssistantText != "next partial" {
		t.Fatalf("next-turn progress = %+v, %v; want turn 2", got, ok)
	}
}

func TestRecoverProgressRejectsOldTurnNumberWithoutNewerAssistant(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "progress old turn")
	if err := sess.Append(KindAssistant, "turn one"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendProgress(Progress{Turn: 1, AssistantText: "old"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := RecoverProgress(sess); ok {
		t.Fatalf("progress behind committed assistant accepted: %+v", got)
	}
}

func TestSyncProgressPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "sync progress")
	if err := sess.SyncProgress(Progress{Turn: 1, AssistantText: "durable chunk"}); err != nil {
		t.Fatalf("SyncProgress: %v", err)
	}
	id := sess.ID()

	reopened, err := OpenSession(dir, id)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	got, ok := reopened.LatestProgress()
	if !ok || got.Turn != 1 || got.AssistantText != "durable chunk" {
		t.Fatalf("progress after reopen = %+v, %v", got, ok)
	}
	if count := reopened.ProgressCount(); count != 1 {
		t.Fatalf("ProgressCount after reopen = %d, want 1", count)
	}

	// 无参数形式是持久性屏障，绝不能添加记录。
	if err := reopened.SyncProgress(); err != nil {
		t.Fatalf("SyncProgress (barrier): %v", err)
	}
	if count := reopened.ProgressCount(); count != 1 {
		t.Fatalf("ProgressCount after barrier = %d, want 1", count)
	}
}

func TestLatestProgressSurvivesMidTurnCrash(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "crash")
	if err := sess.Append(KindUser, "do work"); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendProgress(Progress{
		Turn: 1, AssistantText: "half streamed", ToolCalls: []string{"patch.write"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendProgress(Progress{Turn: 1, AssistantText: "more streamed"}); err != nil {
		t.Fatal(err)
	}
	id := sess.ID()

	// 模拟崩溃：不执行干净关闭就丢弃所有内存句柄，
	// 并从磁盘上的转录重建会话。
	sess = nil
	reopened, err := OpenSession(dir, id)
	if err != nil {
		t.Fatalf("OpenSession after crash: %v", err)
	}
	if count := reopened.ProgressCount(); count != 2 {
		t.Fatalf("ProgressCount after crash = %d, want 2", count)
	}
	got, ok := reopened.LatestProgress()
	if !ok || got.Turn != 1 || got.AssistantText != "more streamed" {
		t.Fatalf("progress after crash = %+v, %v; want turn 1 / more streamed", got, ok)
	}
	if got, ok := RecoverProgress(reopened); !ok || got.AssistantText != "more streamed" {
		t.Fatalf("recovery after crash = %+v, %v", got, ok)
	}
}
