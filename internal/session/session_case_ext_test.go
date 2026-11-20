package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transcriptStem 剥离末尾 ".jsonl" 的方式，必须与 List/resolvePath 中
// 不区分大小写的发现逻辑完全一致。区分大小写的 strings.TrimSuffix
// 会在 "SESSION.JSONL" 上留下扩展名，从而在区分大小写的文件系统上
// 破坏通过前缀打开的会话的 id。
func TestTranscriptStemCaseInsensitiveExtension(t *testing.T) {
	for name, want := range map[string]string{
		"session.jsonl": "session",
		"SESSION.JSONL": "SESSION",
		"Mixed.Jsonl":   "Mixed",
		"plain":         "plain",
		"a.jsonl.bak":   "a.jsonl.bak",
		".jsonl":        "",
	} {
		if got := transcriptStem(name); got != want {
			t.Errorf("transcriptStem(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestOpenSessionResolvesUppercaseExtensionByPrefix 覆盖集成路径：
// 名为 "Mixed.JSONL" 的转录被 resolvePath 中不区分大小写的
// 前缀查找找到，返回的 Session 必须保留裸 id，
// 使 Session.path() 指向真实文件（而不是 "Mixed.JSONL.jsonl"）。
func TestOpenSessionResolvesUppercaseExtensionByPrefix(t *testing.T) {
	dir := t.TempDir()
	upperPath := filepath.Join(dir, "Mixed.JSONL")
	writeRawSession(t, upperPath, []Record{
		{ID: "n1", Kind: KindHeader, Data: json.RawMessage(`{"title":"upper"}`)},
		{ID: "n2", Parent: "n1", Kind: KindUser, Data: json.RawMessage(`"hello"`)},
	})

	s, err := OpenSession(dir, "Mixed")
	if err != nil {
		t.Fatalf("OpenSession(Mixed): %v", err)
	}
	if got, want := s.ID(), "Mixed"; got != want {
		t.Fatalf("session ID = %q, want %q", got, want)
	}
	if !strings.EqualFold(s.path(), upperPath) {
		t.Fatalf("session path = %q, want %q (case-insensitive)", s.path(), upperPath)
	}
	// 句柄必须保留 ResolvePath 找到的确切拼写。在 Windows 上
	// 这可能是规范的小写拼写（文件系统不区分大小写）；
	// 在 Linux 上是真实的 "Mixed.JSONL"。无论哪种情况，从裸 id 派生路径
	// 都会丢失该信息。
	resolved, err := ResolvePath(dir, "Mixed")
	if err != nil {
		t.Fatalf("ResolvePath(Mixed): %v", err)
	}
	if got := s.path(); got != resolved {
		t.Fatalf("session path = %q, want resolved path %q", got, resolved)
	}
	entries := s.Messages()
	if len(entries) != 1 || entries[0].Content != "hello" {
		t.Fatalf("Messages() = %+v, want one entry with content hello", entries)
	}

	// 追加必须落在原始转录中，并在重新打开时可见。
	if err := s.Append(KindAssistant, "reply"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	reopened, err := OpenSession(dir, "Mixed")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(reopened.Messages()); got != 2 {
		t.Fatalf("reopened Messages() = %d, want 2", got)
	}
	// 不得创建扩展名加倍的幽灵转录。
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range dirEntries {
		if strings.Contains(strings.ToLower(entry.Name()), "jsonl.jsonl") {
			t.Fatalf("phantom transcript created: %s", entry.Name())
		}
	}
}

// TestOpenSessionPrefixKeepsActualFileName 在每个平台上检验 resolvePath 的
// 目录扫描分支。"Mixe" 即使在 Windows 上也没有精确匹配的文件，
// 因此查找必须回退到扫描，并返回真实的条目名
// "Mixed.JSONL"。从该路径构建的句柄必须读取并追加到同一个文件；
// 重建 dir/id+".jsonl" 会丢失扩展名的大小写，并会
// 在 Linux 上静默指向另一个（或不存在的）文件。
func TestOpenSessionPrefixKeepsActualFileName(t *testing.T) {
	dir := t.TempDir()
	upperPath := filepath.Join(dir, "Mixed.JSONL")
	writeRawSession(t, upperPath, []Record{
		{ID: "n1", Kind: KindHeader, Data: json.RawMessage(`{"title":"upper"}`)},
		{ID: "n2", Parent: "n1", Kind: KindUser, Data: json.RawMessage(`"hello"`)},
	})

	s, err := OpenSession(dir, "Mixe")
	if err != nil {
		t.Fatalf("OpenSession(Mixe): %v", err)
	}
	if got := s.path(); got != upperPath {
		t.Fatalf("session path = %q, want exact resolved path %q", got, upperPath)
	}
	if got, want := s.ID(), "Mixed"; got != want {
		t.Fatalf("session ID = %q, want %q", got, want)
	}
	if entries := s.Messages(); len(entries) != 1 || entries[0].Content != "hello" {
		t.Fatalf("Messages() = %+v, want one entry with content hello", entries)
	}
	if err := s.Append(KindAssistant, "reply"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	data, err := os.ReadFile(upperPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", upperPath, err)
	}
	if !strings.Contains(string(data), "reply") {
		t.Fatalf("append did not land in %s: %s", upperPath, data)
	}
	if _, err := os.Stat(filepath.Join(dir, "Mixe.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("phantom transcript created for prefix, stat err = %v", err)
	}
}
