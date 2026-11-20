package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// archiveTestMessages mirrors the mid-turn compaction fixture: enough distinct
// content that the dropped prefix can be recovered verbatim from the archive.
func archiveTestMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "goal: build the lexer"},
		{Role: "assistant", Content: "后续: add the lexer"},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", 400)},
		{Role: "assistant", Content: "continue"},
		{Role: "tool", ToolCallID: "c2", Content: strings.Repeat("y", 400)},
	}
}

func archiveTestSession(t *testing.T, res Resolved, messages []llm.Message) *session.Session {
	t.Helper()
	store := testStore(t, res)
	sess, err := store.NewSession("archive-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if err := sess.Append(m.Role, map[string]any{
			"content": m.Content, "tool_call_id": m.ToolCallID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return sess
}

func latestCompaction(t *testing.T, sess *session.Session) session.Compaction {
	t.Helper()
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Kind == session.KindCompaction {
			var c session.Compaction
			if err := json.Unmarshal(records[i].Data, &c); err != nil {
				t.Fatal(err)
			}
			return c
		}
	}
	t.Fatal("no compaction record found")
	return session.Compaction{}
}

func TestMaybeCompactWritesLosslessArchiveWhenEnabled(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res.Cfg.Raw = map[string]any{"compaction": map[string]any{"archive": true}}

	messages := archiveTestMessages()
	sess := archiveTestSession(t, res, messages)
	out, changed := newSessionCompactor(res, sess).MaybeCompact(messages, 150)
	if !changed {
		t.Fatal("expected a compaction for a small window")
	}

	compaction := latestCompaction(t, sess)
	ref := compaction.Archive
	if ref == nil {
		t.Fatal("Archive reference was not attached to the compaction record")
	}
	if !strings.HasPrefix(ref.Path, "compactions/") || !strings.HasSuffix(ref.Path, ".jsonl") {
		t.Fatalf("archive path = %q, want a store-relative compactions/*.jsonl", ref.Path)
	}
	if ref.Bytes <= 0 {
		t.Fatalf("archive byte size = %d, want > 0", ref.Bytes)
	}

	// The archive file exists under the session store and its size matches.
	onDisk := filepath.Join(sess.Dir(), filepath.FromSlash(ref.Path))
	info, err := os.Stat(onDisk)
	if err != nil {
		t.Fatalf("archive file %s missing: %v", onDisk, err)
	}
	if info.Size() != ref.Bytes {
		t.Fatalf("archive size on disk = %d, ref says %d", info.Size(), ref.Bytes)
	}

	// Every dropped entry has a contiguous, non-empty byte range, and reading
	// the archive back recovers the original messages verbatim.
	entries := entriesFromMessages(messages)
	start := session.CutForBudget(entries, 150)
	if start <= 0 {
		t.Fatal("cut produced no dropped messages")
	}
	if len(ref.Ranges) != start {
		t.Fatalf("archive ranges = %d, want %d dropped messages", len(ref.Ranges), start)
	}
	var cursor int64
	for i, rng := range ref.Ranges {
		if rng.Start != cursor || rng.End <= rng.Start {
			t.Fatalf("range %d = [%d,%d), expected to start at %d", i, rng.Start, rng.End, cursor)
		}
		cursor = rng.End
	}
	recovered, err := session.ReadCompactionArchive(sess.Dir(), ref.Path)
	if err != nil {
		t.Fatalf("read back archive: %v", err)
	}
	if len(recovered) != start {
		t.Fatalf("recovered %d messages, want %d", len(recovered), start)
	}
	for i := 0; i < start; i++ {
		if recovered[i].Role != entries[i].Role || recovered[i].Content != entries[i].Content {
			t.Fatalf("recovered message %d = (%s,%q), want (%s,%q)",
				i, recovered[i].Role, recovered[i].Content, entries[i].Role, entries[i].Content)
		}
	}

	// The recovery notice is part of both the stored summary and the folded view.
	if !strings.Contains(compaction.Summary, "## Lossless archive") ||
		!strings.Contains(compaction.Summary, ref.Path) {
		t.Fatalf("summary lacks the archive recovery notice:\n%s", compaction.Summary)
	}
	if len(out) == 0 || out[0].Content != compaction.Summary {
		t.Fatal("folded view does not carry the archive notice")
	}
}

func TestMaybeCompactArchiveIsOffByDefault(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	messages := archiveTestMessages()
	sess := archiveTestSession(t, res, messages)
	out, changed := newSessionCompactor(res, sess).MaybeCompact(messages, 150)
	if !changed {
		t.Fatal("expected a compaction for a small window")
	}

	compaction := latestCompaction(t, sess)
	if compaction.Archive != nil {
		t.Fatal("Archive must stay nil when archiving is off")
	}
	if strings.Contains(compaction.Summary, "## Lossless archive") {
		t.Fatalf("default summary must not carry the archive notice:\n%s", compaction.Summary)
	}
	if _, err := os.Stat(filepath.Join(sess.Dir(), "compactions")); !os.IsNotExist(err) {
		t.Fatalf("no compactions directory should be created by default (stat err: %v)", err)
	}
	if len(out) == 0 || out[0].Content != compaction.Summary {
		t.Fatal("folded view must still carry the stored summary")
	}
}

func TestCompactionArchiveEnabledSwitchShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{"missing", nil, false},
		{"nested true", map[string]any{"compaction": map[string]any{"archive": true}}, true},
		{"nested false", map[string]any{"compaction": map[string]any{"archive": false}}, false},
		{"nested other key", map[string]any{"compaction": map[string]any{"threshold": 100}}, false},
		{"top camel", map[string]any{"archiveCompactions": true}, true},
		{"top kebab", map[string]any{"archive-compactions": true}, true},
		{"wrong type", map[string]any{"compaction": map[string]any{"archive": "true"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactionArchiveEnabled(tc.raw); got != tc.want {
				t.Fatalf("compactionArchiveEnabled(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCompactionArchivePathStaysInsideStore(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res.Cfg.Raw = map[string]any{"archiveCompactions": true}

	messages := archiveTestMessages()
	sess := archiveTestSession(t, res, messages)
	if _, changed := newSessionCompactor(res, sess).MaybeCompact(messages, 150); !changed {
		t.Fatal("expected a compaction")
	}
	ref := latestCompaction(t, sess).Archive
	if ref == nil {
		t.Fatal("expected an archive")
	}

	// The produced reference resolves under the store dir, never above it.
	full, err := session.CompactionArchivePath(sess.Dir(), strings.TrimSuffix(strings.TrimPrefix(ref.Path, "compactions/"), ".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(sess.Dir(), full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("archive path %s escapes store dir %s (rel %s, err %v)", full, sess.Dir(), rel, err)
	}

	// A malicious stored path cannot read out of the store.
	if _, err := session.ReadCompactionArchive(sess.Dir(), "../../outside.jsonl"); err == nil {
		t.Fatal("ReadCompactionArchive must reject a path escaping the store")
	}
	// A malicious id cannot write out of the store either.
	if _, err := session.WriteCompactionArchiveRef(sess.Dir(), "../escape", entriesFromMessages(messages)); err == nil {
		t.Fatal("WriteCompactionArchiveRef must reject an escaping id")
	}
}

func TestLegacyCompactionRecordWithoutArchiveStillReads(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sess := archiveTestSession(t, res, nil)

	// An old-style record has no "archive" field at all.
	legacy := `{"summary":"## Compaction\n- dropped 2 message(s), kept the last 1","dropped":2,"keep":1}`
	if err := sess.Append(session.KindCompaction, json.RawMessage(legacy)); err != nil {
		t.Fatal(err)
	}
	compaction := latestCompaction(t, sess)
	if compaction.Archive != nil {
		t.Fatal("legacy record unexpectedly has an archive")
	}
	if !strings.Contains(compaction.Summary, "dropped 2 message(s)") {
		t.Fatalf("legacy summary lost: %q", compaction.Summary)
	}
	// Messages() projects a nil-Archive compaction into the system summary just
	// as before; no regression in the read path.
	entries := sess.Messages()
	if len(entries) == 0 || !strings.HasPrefix(entries[0].Content, "## Compaction") {
		t.Fatalf("legacy compaction did not project to a summary: %+v", entries)
	}
}
