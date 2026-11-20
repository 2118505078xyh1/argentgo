package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	// 内存中的视图也经过脱敏。
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

	// SetDir 将已有 bank 指向另一个目录。
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

func TestReflectRanksSentencesByQueryOverlap(t *testing.T) {
	bank := NewBank(t.TempDir())
	if _, err := bank.Retain("unrelated intro. alpha beta gamma are here. trailing note.", nil, ""); err != nil {
		t.Fatal(err)
	}
	out := bank.Reflect("alpha beta gamma", 5)
	if !strings.HasPrefix(out, "Reflection (extractive synthesis):\n") {
		t.Fatalf("missing header in %q", out)
	}
	lines := strings.Split(strings.TrimPrefix(out, "Reflection (extractive synthesis):\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %#v, want three sentences", lines)
	}
	// 相关句子必须排在前面，即使它不是记忆中的第一句：
	// 重叠程度胜过较早句子的微小加分。
	if !strings.Contains(lines[0], "alpha beta gamma are here") {
		t.Fatalf("top sentence = %q, want the query-overlapping one", lines[0])
	}
	if !strings.Contains(lines[1], "unrelated intro") || !strings.Contains(lines[2], "trailing note") {
		t.Fatalf("remaining sentences out of order: %#v", lines)
	}
}

func TestReflectDedupesNearIdenticalSentences(t *testing.T) {
	bank := NewBank(t.TempDir())
	if _, err := bank.Retain("same fact here. SAME  fact here. another sentence.", nil, ""); err != nil {
		t.Fatal(err)
	}
	out := strings.ToLower(bank.Reflect("fact", 5))
	if n := strings.Count(out, "same fact here"); n != 1 {
		t.Fatalf("dedupe produced %d copies of the sentence in %q", n, out)
	}
	if !strings.Contains(out, "another sentence") {
		t.Fatalf("distinct sentence missing from %q", out)
	}
}

func TestReflectCJKQuery(t *testing.T) {
	bank := NewBank(t.TempDir())
	if _, err := bank.Retain("我喜欢Go。Go很快。Python也不错。", nil, ""); err != nil {
		t.Fatal(err)
	}
	out := bank.Reflect("喜欢", 5)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "我喜欢Go") {
		t.Fatalf("CJK reflection = %q, want 我喜欢Go ranked first", out)
	}
	if !strings.Contains(out, "Go很快") {
		t.Fatalf("CJK reflection = %q, want the second CJK sentence too", out)
	}
}

func TestReflectEmptyBank(t *testing.T) {
	if got := NewBank(t.TempDir()).Reflect("anything", 5); got != "no memories to reflect on" {
		t.Fatalf("empty reflection = %q", got)
	}
}

func TestReflectIsBoundedAndGraphemeSafe(t *testing.T) {
	bank := NewBank(t.TempDir())
	text := strings.Repeat("x", 4050) + "e\u0301🚀tail."
	if _, err := bank.Retain(text, nil, ""); err != nil {
		t.Fatal(err)
	}
	out := bank.Reflect("", 10)
	if len(out) > maxReflectBytes {
		t.Fatalf("reflection is %d bytes, cap is %d", len(out), maxReflectBytes)
	}
	if !utf8.ValidString(out) {
		t.Fatal("reflection is not valid UTF-8")
	}
	// 上限落在文本内部，但放得下的组合序列和 emoji
	// 必须完整输出，绝不能被切掉一半。
	if !strings.Contains(out, "e\u0301🚀") {
		t.Fatalf("reflection split a grapheme cluster: %q", out)
	}
	if strings.Contains(out, "tail.") {
		t.Fatalf("reflection was not clipped at the cap: %q", out)
	}
}

func TestReflectIsDeterministic(t *testing.T) {
	bank := NewBank(t.TempDir())
	for _, text := range []string{"first fact about alpha.", "beta fact about gamma.", "alpha and beta again."} {
		if _, err := bank.Retain(text, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	want := bank.Reflect("alpha beta", 3)
	for i := 0; i < 100; i++ {
		if got := bank.Reflect("alpha beta", 3); got != want {
			t.Fatalf("call %d = %q, want %q", i, got, want)
		}
	}
}
