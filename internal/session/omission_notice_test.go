package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOmissionAuthorityNotice 验证省略告示：干净会话逐字节不变；
// 发生压缩丢消息或隐藏编辑时恰好注入一次，且不含被省略的原文。
func TestOmissionAuthorityNotice(t *testing.T) {
	const secret = "secret-only-in-dropped-history"

	t.Run("clean session unchanged", func(t *testing.T) {
		s := newTestSession(t, t.TempDir(), "notice-clean")
		if _, err := s.AppendRecord(KindUser, "hello"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendRecord(KindAssistant, "world"); err != nil {
			t.Fatal(err)
		}
		for _, entry := range s.Messages() {
			if entry.ID == OmissionNoticeID || strings.Contains(entry.Content, "omission_authority_notice") {
				t.Fatal("clean projection unexpectedly contains an omission notice")
			}
		}
	})

	t.Run("compaction drop injects once", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "notice-compact"+fileExt)
		ts := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
		compaction, err := json.Marshal(Compaction{Summary: "summary", Dropped: 1, Keep: 1})
		if err != nil {
			t.Fatal(err)
		}
		records := []Record{
			{ID: "n000001", Kind: KindHeader, TS: ts, Data: json.RawMessage(`{"title":"t","created":"2026-11-23T09:00:00Z"}`)},
			{ID: "n000002", Parent: "n000001", Kind: KindUser, TS: ts, Data: json.RawMessage(`{"content":"` + secret + `"}`)},
			{ID: "n000003", Parent: "n000002", Kind: KindUser, TS: ts, Data: json.RawMessage(`{"content":"current request"}`)},
			{ID: "n000004", Parent: "n000003", Kind: KindCompaction, TS: ts, Data: compaction},
		}
		var buf bytes.Buffer
		for _, record := range records {
			line, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := OpenSession(dir, "notice-compact")
		if err != nil {
			t.Fatal(err)
		}
		messages := s.MessagesWithOmissionNotice()
		count := 0
		for _, entry := range messages {
			if entry.ID == OmissionNoticeID {
				count++
				if entry.Content != OmissionNoticeText {
					t.Fatalf("notice text changed: %q", entry.Content)
				}
			}
			if strings.Contains(entry.Content, secret) {
				t.Fatal("dropped original content leaked into projection")
			}
		}
		if count != 1 {
			t.Fatalf("omission notice count = %d, want 1", count)
		}
		if messages[len(messages)-1].ID != OmissionNoticeID {
			t.Fatal("omission notice is not the last projected message")
		}
	})

	t.Run("hidden edit injects notice", func(t *testing.T) {
		s := newTestSession(t, t.TempDir(), "notice-hidden")
		first, err := s.AppendRecord(KindUser, "to be hidden")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendContextEdit(ContextEdit{TargetID: first.ID, Text: ""}); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range s.MessagesWithOmissionNotice() {
			if entry.ID == OmissionNoticeID {
				found = true
			}
		}
		if !found {
			t.Fatal("hidden context edit did not trigger an omission notice")
		}
	})
}
