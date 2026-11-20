package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transcriptStem must strip a trailing ".jsonl" exactly as the case-insensitive
// discovery in List/resolvePath matches it. A case-sensitive strings.TrimSuffix
// leaves the extension on "SESSION.JSONL", which corrupts the id of a session
// opened through a prefix on case-sensitive filesystems.
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

// TestOpenSessionResolvesUppercaseExtensionByPrefix covers the integration
// path: a transcript named "Mixed.JSONL" is found by the case-insensitive
// prefix lookup in resolvePath, and the returned Session must keep the bare id
// so Session.path() points at the real file (not "Mixed.JSONL.jsonl").
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
	// The handle must keep the exact spelling ResolvePath found. On Windows
	// that may be the canonical lower-case spelling (the FS is insensitive);
	// on Linux it is the real "Mixed.JSONL". Either way, deriving the path
	// from the bare id would lose that information.
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

	// An append must land in the original transcript and be visible on reopen.
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
	// No phantom transcript with a doubled extension may be created.
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

// TestOpenSessionPrefixKeepsActualFileName exercises the directory-scan branch
// of resolvePath on every platform. "Mixe" has no exact file even on Windows,
// so the lookup must fall back to the scan and return the real entry name
// "Mixed.JSONL". A handle built from that path must read and append to the very
// same file; reconstructing dir/id+".jsonl" loses the extension case and would
// silently point at a different (or missing) file on Linux.
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
