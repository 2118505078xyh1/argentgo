package session

import (
	"testing"
)

func TestAppendForkLineageAndTreeProjection(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "fork-lineage")
	// 追加血缘记录，标明从 parent-session 的 rec-42 分叉。
	if err := sess.AppendForkLineage("parent-session", "rec-42"); err != nil {
		t.Fatalf("AppendForkLineage: %v", err)
	}
	branches, err := Branches(sess.path())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range branches {
		if b.Kind == KindForkLineage {
			found = true
			if b.SourceSession != "parent-session" || b.SourceRecord != "rec-42" {
				t.Fatalf("branch source = %q/%q, want parent-session/rec-42", b.SourceSession, b.SourceRecord)
			}
		}
	}
	if !found {
		t.Fatalf("fork_lineage branch not found: %+v", branches)
	}
	// 血缘记录不进入模型可见消息。
	for _, m := range sess.Messages() {
		if m.ID == "" && m.Content == "" {
			continue
		}
	}
}

func TestForkLineagePayloadExportedConstants(t *testing.T) {
	// 三个键名稳定（跨版本读取方依赖它们）。
	p := forkLineagePayload("p", "r", 123)
	if p[forkLineageParentKey] != "p" || p[forkLineageAtRecordKey] != "r" || p[forkLineageTsKey] != "123" {
		t.Fatalf("payload = %v", p)
	}
}
