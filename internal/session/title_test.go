package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestTitleFromPromptRedactsAndTruncatesAtRuneBoundary(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  布置  任务\n下一行 ", "布置 任务 下一行"},
		{"token = 2118505078xyh1", "token = [redacted:token]"},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := TitleFromPrompt(tc.in); got != tc.want {
			t.Errorf("TitleFromPrompt(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("界", 90)
	title := TitleFromPrompt(long)
	if len([]rune(title)) != titleLimit {
		t.Fatalf("title runes = %d, want %d", len([]rune(title)), titleLimit)
	}
}

func TestTitleOverrideWinsAndForkInheritsIt(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if got := sess.Title(); got != "first prompt" {
		t.Fatalf("derived title = %q", got)
	}
	if err := sess.SetTitle("  explicit name  "); err != nil {
		t.Fatal(err)
	}
	if got := sess.Title(); got != "explicit name" {
		t.Fatalf("override title = %q", got)
	}
	metas, err := store.List()
	if err != nil || len(metas) != 1 || metas[0].Title != "explicit name" {
		t.Fatalf("list meta = %+v, %v", metas, err)
	}
	fork, err := sess.Fork("n000002")
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "explicit name" {
		t.Fatalf("fork title = %q", got)
	}
	if err := fork.SetTitle(""); err != nil {
		t.Fatal(err)
	}
	if got := fork.Title(); got != "first prompt" {
		t.Fatalf("cleared title = %q", got)
	}
}

func TestLegacyHeaderAndAttemptTitlesStayReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "n1", Kind: KindHeader, Data: json.RawMessage(`{"title":"header wins"}`)},
		{ID: "n2", Parent: "n1", Kind: KindUser, Data: json.RawMessage(`"prompt text"`)},
		{ID: "n3", Parent: "n2", Kind: KindAttempt, Data: json.RawMessage(`{"reason":"failed"}`)},
	})
	sess, err := OpenSession(dir, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.Title(); got != "header wins" {
		t.Fatalf("title = %q", got)
	}
}
