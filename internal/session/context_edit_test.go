package session

import (
	"strings"
	"testing"
)

// newEditSession 建一个含两条 assistant 消息的会话，返回它们的记录 ID。
func newEditSession(t *testing.T) (*Session, string, string) {
	t.Helper()
	sess := newTestSession(t, t.TempDir(), "edit-context")
	id1 := appendAndID(t, sess, KindUser, "question 1")
	id2 := appendAndID(t, sess, KindAssistant, "original answer")
	_ = id1
	return sess, id2, ""
}

func appendAndID(t *testing.T, sess *Session, kind string, payload any) string {
	t.Helper()
	rec, err := sess.AppendRecord(kind, payload)
	if err != nil {
		t.Fatalf("AppendRecord(%s): %v", kind, err)
	}
	return rec.ID
}

func TestContextEditReplaceAndLastWins(t *testing.T) {
	sess, target, _ := newEditSession(t)
	// 第一次编辑：替换文本。
	if err := sess.AppendContextEdit(ContextEdit{TargetID: target, Text: "first edit"}); err != nil {
		t.Fatalf("AppendContextEdit: %v", err)
	}
	// 第二次编辑同一目标：last-wins。
	if err := sess.AppendContextEdit(ContextEdit{TargetID: target, Text: "final answer"}); err != nil {
		t.Fatalf("AppendContextEdit(2): %v", err)
	}

	// 模型可见投影显示最后一次编辑；原始记录字节不变。
	msgs := sess.Messages()
	found := false
	for _, m := range msgs {
		if m.ID == target {
			found = true
			if m.Content != "final answer" {
				t.Fatalf("projected content = %q, want %q", m.Content, "final answer")
			}
		}
	}
	if !found {
		t.Fatalf("target %q not in projection: %+v", target, msgs)
	}
	raw, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range raw {
		if r.ID == target && strings.Contains(string(r.Data), "original answer") {
			// 原记录必须仍含原始文本（非破坏性）。
			return
		}
	}
	t.Fatal("original record bytes were modified")
}

func TestContextEditHide(t *testing.T) {
	sess, target, _ := newEditSession(t)
	if err := sess.AppendContextEdit(ContextEdit{TargetID: target, Text: ""}); err != nil {
		t.Fatal(err)
	}
	for _, m := range sess.Messages() {
		if m.ID == target {
			t.Fatalf("hidden entry still visible: %+v", m)
		}
	}
}

func TestContextEditNoEditUnchanged(t *testing.T) {
	sess, _, _ := newEditSession(t)
	msgs := sess.Messages()
	found := false
	for _, m := range msgs {
		if m.Content == "original answer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no-edit projection changed: %+v", msgs)
	}
}

func TestContextEditRejectsEmptyTarget(t *testing.T) {
	sess, _, _ := newEditSession(t)
	if err := sess.AppendContextEdit(ContextEdit{TargetID: "", Text: "x"}); err == nil {
		t.Fatal("empty target should error")
	}
}

func TestContextEditSanitizes(t *testing.T) {
	got := sanitizeContextEdit(ContextEdit{TargetID: "  t  ", Text: "  hello  "})
	if got.TargetID != "t" || got.Text != "  hello  " {
		t.Fatalf("sanitized = %+v", got)
	}
}
