package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttemptRecordsAreDurableButNotModelVisible(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "attempts")
	if err := sess.Append(KindUser, "prompt"); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{AttemptFailed, AttemptCancelled, AttemptStreamError, AttemptRetried} {
		if _, err := sess.AppendAttempt(NewAttempt(reason, errors.New("boom"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.Append(KindAssistant, "final answer"); err != nil {
		t.Fatal(err)
	}

	messages := sess.Messages()
	if len(messages) != 2 || messages[0].Content != "prompt" || messages[1].Content != "final answer" {
		t.Fatalf("model history = %+v", messages)
	}
	attempts, err := sess.Attempts()
	if err != nil || len(attempts) != 4 {
		t.Fatalf("attempts = %+v, %v", attempts, err)
	}
	for i, want := range []string{AttemptFailed, AttemptCancelled, AttemptStreamError, AttemptRetried} {
		if attempts[i].Reason != want || attempts[i].ErrorClass != "*errors.errorString" {
			t.Fatalf("attempt %d = %+v", i, attempts[i])
		}
	}
}

func TestAppendAttemptRedactsErrorTextBeforeWriting(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "redact")
	if _, err := sess.AppendAttempt(NewAttempt(AttemptFailed, errors.New("token = FAKE_SECRET_VALUE"))); err != nil {
		t.Fatal(err)
	}
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	raw := string(records[len(records)-1].Data)
	if strings.Contains(raw, "FAKE_SECRET_VALUE") || !strings.Contains(raw, "[redacted:") {
		t.Fatalf("stored attempt = %s", raw)
	}
}

func TestSearchReturnsRedactedAttemptSnippets(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "attempts.jsonl"), []Record{{
		ID: "n1", Kind: KindAttempt,
		Data: []byte(`{"reason":"stream_error","message":"token = FAKE_SECRET_VALUE in body"}`),
	}})
	hits, err := Search(dir, "token", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	if strings.Contains(hits[0].Snippet, "FAKE_SECRET_VALUE") || !strings.Contains(hits[0].Snippet, "redacted") {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
}

func TestCompactionPreservesAttemptRecordsAndHTMLExport(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "compact attempts")
	for i := 0; i < 6; i++ {
		if err := sess.Append(KindUser, "question"); err != nil {
			t.Fatal(err)
		}
		if err := sess.Append(KindAssistant, "answer"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sess.AppendAttempt(NewAttempt(AttemptStreamError, errors.New("stream broke"))); err != nil {
		t.Fatal(err)
	}
	before, err := sess.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Compact(2); err != nil {
		t.Fatal(err)
	}
	after, err := sess.Attempts()
	if err != nil || len(after) != len(before) {
		t.Fatalf("attempts after compaction = %+v, %v", after, err)
	}
	for _, attempt := range after {
		if attempt.Reason != AttemptStreamError || StringsContainSecret(attempt.Message) {
			t.Fatalf("attempt after compaction = %+v", attempt)
		}
	}
	var html strings.Builder
	if err := RenderHTML(filepath.Join(dir, sess.ID()+fileExt), "", &html); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), "attempt") || !strings.Contains(html.String(), "stream broke") {
		t.Fatalf("html export lacks attempt section: %s", html.String())
	}
}

func StringsContainSecret(text string) bool {
	return strings.Contains(text, "FAKE_SECRET_VALUE")
}

func TestRecordAttemptErrorRejectsEmptyReason(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "empty reason")
	if _, err := sess.AppendAttempt(Attempt{}); err == nil {
		t.Fatal("empty attempt reason was accepted")
	}
	if _, err := os.Stat(filepath.Join(sess.Dir(), sess.ID()+fileExt)); err != nil {
		t.Fatal(err)
	}
}
