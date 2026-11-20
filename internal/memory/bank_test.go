package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainRecallRoundtrip(t *testing.T) {
	bank := NewBank(t.TempDir())
	id, err := bank.Retain("the quick brown fox jumps over the lazy dog", []string{"animals"}, "unit test")
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	if id == "" {
		t.Fatal("retain returned an empty id")
	}
	got := bank.Recall("quick brown fox", nil, 10)
	if len(got) != 1 {
		t.Fatalf("recall = %#v, want one hit", got)
	}
	if got[0].ID != id || got[0].Text != "the quick brown fox jumps over the lazy dog" {
		t.Fatalf("recall = %#v, want retained memory", got[0])
	}
	if len(got[0].Tags) != 1 || got[0].Tags[0] != "animals" {
		t.Fatalf("tags = %v, want [animals]", got[0].Tags)
	}
	if got[0].Source != "unit test" {
		t.Fatalf("source = %q, want %q", got[0].Source, "unit test")
	}
}

func TestRecallRanksByRelevance(t *testing.T) {
	bank := NewBank(t.TempDir())
	if _, err := bank.Retain("alpha beta gamma delta", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := bank.Retain("alpha alpha alpha", nil, ""); err != nil {
		t.Fatal(err)
	}
	got := bank.Recall("alpha", nil, 10)
	if len(got) != 2 {
		t.Fatalf("recall = %#v, want two hits", got)
	}
	if got[0].Text != "alpha alpha alpha" {
		t.Fatalf("top hit = %q, want the memory with the most matches", got[0].Text)
	}
}

func TestRecallTagFilter(t *testing.T) {
	bank := NewBank(t.TempDir())
	for _, item := range []struct {
		text string
		tags []string
	}{
		{"work note", []string{"work"}},
		{"home note", []string{"home"}},
		{"shared note", []string{"work", "home"}},
	} {
		if _, err := bank.Retain(item.text, item.tags, ""); err != nil {
			t.Fatal(err)
		}
	}
	got := bank.Recall("", []string{"work"}, 10)
	if len(got) != 2 {
		t.Fatalf("recall = %#v, want two tagged hits", got)
	}
	for _, mem := range got {
		if mem.Text == "home note" {
			t.Fatalf("untagged memory leaked into tag filter: %#v", got)
		}
	}
}

func TestForgetRemovesMemory(t *testing.T) {
	bank := NewBank(t.TempDir())
	id1, err := bank.Retain("keep me", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := bank.Retain("forget me", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := bank.Forget(id2); err != nil {
		t.Fatalf("forget: %v", err)
	}
	all := bank.All()
	if len(all) != 1 || all[0].ID != id1 {
		t.Fatalf("all = %#v, want only the kept memory", all)
	}
	if err := bank.Forget(id2); err != nil {
		t.Fatalf("forget must be idempotent: %v", err)
	}
	if err := bank.Forget(""); err == nil {
		t.Fatal("forget with empty id must fail")
	}
}

func TestCapDropsOldestEntries(t *testing.T) {
	bank := NewBank(t.TempDir())
	bank.cap = 3
	for _, text := range []string{"one", "two", "three", "four", "five"} {
		if _, err := bank.Retain(text, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	all := bank.All()
	if len(all) != 3 {
		t.Fatalf("all = %#v, want 3 entries after cap", all)
	}
	want := []string{"three", "four", "five"}
	for i, text := range want {
		if all[i].Text != text {
			t.Fatalf("entry %d = %q, want %q", i, all[i].Text, text)
		}
	}
}

func TestMalformedLineSkippedLeniently(t *testing.T) {
	bank := NewBank(t.TempDir())
	valid := `{"id":"x1","text":"kept fact","tags":["ok"],"ts":"2025-01-01T00:00:00Z"}`
	body := "this is not json\n" + valid + "\n" + `{"id":""}` + "\n"
	if err := os.WriteFile(bank.path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	all := bank.All()
	if len(all) != 1 || all[0].Text != "kept fact" {
		t.Fatalf("all = %#v, want only the valid record", all)
	}
	if got := bank.Recall("kept", nil, 5); len(got) != 1 {
		t.Fatalf("recall = %#v, want one hit", got)
	}
}

func TestRetainRedactsCredentials(t *testing.T) {
	bank := NewBank(t.TempDir())
	secret := "key sk-abcdefghijklmnop ghp_abcdefghijklmnop Bearer abc123secret"
	id, err := bank.Retain(secret, []string{"sk-1234567890abcdef"}, "ghp_source_secret")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bank.path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, leaked := range []string{"sk-abcdefghijklmnop", "ghp_abcdefghijklmnop", "abc123secret", "sk-1234567890abcdef", "ghp_source_secret"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("credential %q leaked into memory file: %s", leaked, body)
		}
	}
	if !strings.Contains(body, "[redacted:") {
		t.Fatalf("no redaction marker in memory file: %s", body)
	}
	// The in-memory view is redacted too.
	all := bank.All()
	if len(all) != 1 || all[0].ID != id || strings.Contains(all[0].Text, "sk-abcdef") {
		t.Fatalf("all = %#v, want redacted memory", all)
	}
}

func TestEmptyQueryReturnsNewestFirst(t *testing.T) {
	bank := NewBank(t.TempDir())
	for _, text := range []string{"first", "second", "third"} {
		if _, err := bank.Retain(text, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	got := bank.Recall("", nil, 10)
	if len(got) != 3 {
		t.Fatalf("recall = %#v, want all three", got)
	}
	want := []string{"third", "second", "first"}
	for i, text := range want {
		if got[i].Text != text {
			t.Fatalf("result %d = %q, want %q", i, got[i].Text, text)
		}
	}
}

func TestRetainGeneratesUniqueIDs(t *testing.T) {
	bank := NewBank(t.TempDir())
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		id, err := bank.Retain("repeated fact", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestBankUsesCustomDirectory(t *testing.T) {
	dirA := filepath.Join(t.TempDir(), "nested", "a")
	bankA := NewBank(dirA)
	if _, err := bankA.Retain("only in A", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dirA, memoryFileName)); err != nil {
		t.Fatalf("memory file missing in custom dir: %v", err)
	}

	bankB := NewBank(filepath.Join(t.TempDir(), "nested", "b"))
	if got := bankB.All(); len(got) != 0 {
		t.Fatalf("bank B sees A's memories: %#v", got)
	}

	// SetDir repoints an existing bank at another directory.
	dirC := t.TempDir()
	bankA.SetDir(dirC)
	if bankA.Dir() != dirC {
		t.Fatalf("dir = %q, want %q", bankA.Dir(), dirC)
	}
	if got := bankA.All(); len(got) != 0 {
		t.Fatalf("repointed bank still sees old memories: %#v", got)
	}
}

func TestRetainCapsTextLength(t *testing.T) {
	bank := NewBank(t.TempDir())
	if _, err := bank.Retain(strings.Repeat("x", 10000), nil, ""); err != nil {
		t.Fatal(err)
	}
	all := bank.All()
	if len(all) != 1 {
		t.Fatalf("all = %#v, want one entry", all)
	}
	if len(all[0].Text) > maxTextBytes {
		t.Fatalf("stored %d bytes, cap is %d", len(all[0].Text), maxTextBytes)
	}
}

func TestRetainRejectsEmptyText(t *testing.T) {
	bank := NewBank(t.TempDir())
	if _, err := bank.Retain("   ", nil, ""); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("err = %v, want ErrEmptyText", err)
	}
}
